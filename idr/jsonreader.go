package idr

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/antchfx/xpath"
	"github.com/jf-tech/go-corelib/caches"
	"github.com/jf-tech/go-corelib/ios"
)

// AncestorFieldsNodeName is the reserved name of the node the JSONStreamReader adds
// underneath each streamed target node to host the declared ancestor fields (see
// AncestorField). The input must not use this name for a property of a streamed target.
const AncestorFieldsNodeName = "__ancestors"

// AncestorField declares a field on the ancestor chain of each streamed target node
// whose value should be carried onto the target node itself, so that a record can be
// delivered together with the ancestor data it belongs to (for instance, a line item
// delivered together with the batch number of the batch object it is embedded in).
//
// XPath is evaluated with the streamed target node as the context node, so an ancestor
// field is typically declared with a relative path such as "../batch_id" or
// "../../batch_id". The resolved node (and its subtree, if any) is copied underneath
// the reserved AncestorFieldsNodeName node added to the target node; it can then be
// extracted in transform_declarations using an xpath such as "__ancestors/batch_id".
//
// Contract (all of it is enforced while streaming; see doc/json_xml_in_depth.md):
//   - A node is only visible to the streaming reader once it has been decoded. Since
//     JSON object members are decoded in document order, an ancestor field that appears
//     *after* the streamed target in the input cannot be attached to an already
//     emitted target. When Required is true such a miss is a fatal reader error; when
//     Required is false the field is simply absent from the returned target node.
//   - XPath must resolve to at most one node. Zero nodes follows the Required rule
//     above; more than one node is a fatal reader error.
//   - XPath must resolve to a node outside of the streamed target subtree. Resolving
//     into the target itself is a fatal reader error.
type AncestorField struct {
	// Name is the name of the field under the AncestorFieldsNodeName node.
	Name string
	// XPath is evaluated relative to each streamed target node.
	XPath string
	// Required controls the behavior when XPath resolves to no node.
	Required bool
}

type ancestorField struct {
	name     string
	xpathStr string
	expr     *xpath.Expr
	required bool
}

// JSONStreamReader is a streaming JSON to *Node reader.
type JSONStreamReader struct {
	r                          *ios.LineCountingReader
	d                          *json.Decoder
	xpathExpr, xpathFilterExpr *xpath.Expr
	ancestorFields             []ancestorField
	root, cur, stream          *Node
}

// streamCandidateCheck checks if sp.cur is a potential stream candidate.
// Note even if sp.cur is marked, it only means it could potentially be the
// stream candidate, not guaranteed. E.g., say have a JSON doc looks like this:
//
//	{
//	   "x": {
//	      "a": 5
//	   },
//	   "y": {
//	      "a": 2
//	   }
//	}
//
// And our stream xpath is "/*/a[. < 4]".
// When the reader first encounters "x/a" node, it has the potential to be the
// stream candidate since it matches "/*/a". But we haven't finished processing
// the entire node, thus no way we know if the value of "a" would match the filter
// or not. So we have to mark it as a potential stream candidate and will let
// wrapUpCurAndTargetCheck to do the final check when the entire node of "/x/a"
// is ingested and processed, in which case, "/x/a" will be not be considered
// as stream target, but later "/x/b" will be.
func (sp *JSONStreamReader) streamCandidateCheck() {
	if sp.xpathExpr != nil && sp.stream == nil && MatchAny(sp.root, sp.xpathExpr) {
		sp.stream = sp.cur
	}
}

// wrapUpCurAndTargetCheck wraps sp.cur node processing and also checks if the sp.cur is the stream
// candidate and if it is, then does a final check: a stream candidate is the target if:
// - If it has finished processing (sp.cur == sp.stream)
// - Either we don't have a stream filter xpath or the stream filter xpath matches.
func (sp *JSONStreamReader) wrapUpCurAndTargetCheck() (*Node, error) {
	cur := sp.cur
	// No matter what outcome the wrapUpCurAndTargetCheck() is, the current node is done, and
	// we need to adjust sp.cur to its parent.
	sp.cur = sp.cur.Parent
	// Only do stream target check if the finished cur node is the stream candidate
	if cur != sp.stream {
		return nil, nil
	}
	if sp.xpathFilterExpr == nil || MatchAny(sp.root, sp.xpathFilterExpr) {
		if err := sp.injectAncestorFields(); err != nil {
			// The stream target can never be returned; free it right away so a fatal
			// ancestor field error doesn't leak the (possibly large) record subtree.
			RemoveAndReleaseTree(sp.stream)
			sp.stream = nil
			return nil, err
		}
		return sp.stream, nil
	}
	// This means while the sp.stream was marked as a stream candidate by the initial
	// sp.streamCandidateCheck call, but now we've completed the construction of this
	// node fully and discovered sp.xpathFilterExpr can't be satisfied, so this
	// sp.stream isn't a target. To prevent future mismatch for other stream candidate,
	// we need to remove it from Node tree completely. And reset sp.stream.
	RemoveAndReleaseTree(sp.stream)
	sp.stream = nil
	return nil, nil
}

// cloneJSONSubtree deep-copies a JSON *Node subtree into a newly allocated one.
func cloneJSONSubtree(n *Node) *Node {
	c := CreateJSONNode(n.Type, n.Data, JSONTypeOf(n))
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		AddChild(c, cloneJSONSubtree(child))
	}
	return c
}

// shortJSON renders a node's JSON form for error messages, bounded so a fatal error on a
// large record doesn't produce an unbounded message.
func shortJSON(n *Node) string {
	const maxLen = 256
	s := JSONify2(n)
	if len(s) > maxLen {
		s = s[:maxLen] + "..."
	}
	return s
}

// injectAncestorFields resolves each declared AncestorField against the now-completed stream target
// and copies the resolved ancestor node underneath an AncestorFieldsNodeName node added to the
// target. See AncestorField for the full contract.
func (sp *JSONStreamReader) injectAncestorFields() error {
	if len(sp.ancestorFields) == 0 {
		return nil
	}
	// Ancestor fields are attached as a property of the streamed record, which requires the
	// record itself to be a JSON object.
	if !IsJSONObj(sp.stream) {
		return fmt.Errorf(
			"ancestor fields cannot be attached to non-object record '%s'", shortJSON(sp.stream))
	}
	// The reserved container name must be free on the record, otherwise the injected node
	// would collide with input data.
	for c := sp.stream.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == ElementNode && c.Data == AncestorFieldsNodeName {
			return fmt.Errorf(
				"streamed record '%s' already contains reserved property '%s'",
				shortJSON(sp.stream), AncestorFieldsNodeName)
		}
	}
	// Resolve everything first so injected nodes can't be observed by subsequent queries,
	// and so the streamed record is only mutated once the whole set has been validated.
	names := make([]string, 0, len(sp.ancestorFields))
	nodes := make([]*Node, 0, len(sp.ancestorFields))
	for i := range sp.ancestorFields {
		f := &sp.ancestorFields[i]
		iter := QueryIter(sp.stream, f.expr)
		if !iter.MoveNext() {
			// The ancestor field either doesn't exist at all, or it exists but hasn't been
			// decoded yet (e.g. it appears after the stream target in the input), in which
			// case it isn't yet visible to the streaming reader.
			if f.required {
				return fmt.Errorf(
					"required ancestor field '%s' (xpath: '%s') not found or not yet decoded",
					f.name, f.xpathStr)
			}
			continue
		}
		n := nodeFromIter(iter)
		if iter.MoveNext() {
			return fmt.Errorf(
				"ancestor field '%s' (xpath: '%s') resolves to more than one node", f.name, f.xpathStr)
		}
		// The resolved node must live outside the streamed target, otherwise it will be
		// destroyed together with the target at the next Read() call.
		for a := n; a != nil; a = a.Parent {
			if a == sp.stream {
				return fmt.Errorf(
					"ancestor field '%s' (xpath: '%s') resolves inside the streamed record",
					f.name, f.xpathStr)
			}
		}
		names = append(names, f.name)
		nodes = append(nodes, n)
	}
	if len(nodes) == 0 {
		return nil
	}
	container := CreateJSONNode(ElementNode, AncestorFieldsNodeName, JSONObj)
	AddChild(sp.stream, container)
	for i, n := range nodes {
		jtype := JSONProp
		if IsJSONArr(n) {
			jtype |= JSONArr
		} else if IsJSONObj(n) {
			jtype |= JSONObj
		}
		fieldNode := CreateJSONNode(ElementNode, names[i], jtype)
		if n.Type == TextNode {
			AddChild(fieldNode, CreateJSONNode(TextNode, n.Data, JSONTypeOf(n)))
		} else {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				AddChild(fieldNode, cloneJSONSubtree(c))
			}
		}
		AddChild(container, fieldNode)
	}
	return nil
}

func (sp *JSONStreamReader) addElementChild(data string, jtype JSONType) {
	child := CreateJSONNode(ElementNode, data, jtype)
	AddChild(sp.cur, child)
	sp.cur = child
}

func (sp *JSONStreamReader) addTextChild(tok interface{}) {
	var data string
	var jtype JSONType
	switch v := tok.(type) {
	case float64:
		data = strconv.FormatFloat(v, 'f', -1, 64)
		jtype = JSONValueNum
	case bool:
		data = strconv.FormatBool(v)
		jtype = JSONValueBool
	case nil:
		data = ""
		jtype = JSONValueNull
	default:
		data = v.(string)
		jtype = JSONValueStr
	}
	child := CreateJSONNode(TextNode, data, jtype)
	AddChild(sp.cur, child)
	// Since the child being added is a value node, there won't be anything else
	// added below it, so no need to advance sp.cur to child.
}

func (sp *JSONStreamReader) parseDelim(tok json.Delim) (*Node, error) {
	switch tok {
	case '{':
		switch {
		// arr check needs to be before prop or root check because it's entirely
		// possible sp.cur is a property or root whose value is array type - in that
		// case we want the arr case to be hit first.
		case IsJSONArr(sp.cur):
			// if we see "{" inside an "[]", we create an anonymous object element node
			// to host it.
			sp.addElementChild("", JSONObj)
			sp.streamCandidateCheck()
		case IsJSONProp(sp.cur):
			// a "{" follows a property name, indicate this property's value is an
			// object. Note we don't need to streamCandidateCheck here because we've
			// already done the check when the property itself is processed.
			sp.cur.FormatSpecific = JSONTypeOf(sp.cur) | JSONObj
		case IsJSONRoot(sp.cur):
			// if we see "{" directly on root, make the root node an obj type container
			// and do stream candidate check.
			sp.cur.FormatSpecific = JSONTypeOf(sp.cur) | JSONObj
			sp.streamCandidateCheck()
		}
	case '[':
		switch {
		// Similarly, case order matters.
		case IsJSONArr(sp.cur):
			// if we see "[" inside an "[]" or directly on root, we create an anonymous
			// arr element node to host it.
			sp.addElementChild("", JSONArr)
			sp.streamCandidateCheck()
		case IsJSONProp(sp.cur):
			// Again, similarly we don't do streamCandidateCheck here since the check is already
			// done when the property node is created.
			sp.cur.FormatSpecific = JSONTypeOf(sp.cur) | JSONArr
		case IsJSONRoot(sp.cur):
			// arr directly on root.
			sp.cur.FormatSpecific = JSONTypeOf(sp.cur) | JSONArr
			sp.streamCandidateCheck()
		}
	case '}', ']':
		return sp.wrapUpCurAndTargetCheck()
	}
	return nil, nil
}

func (sp *JSONStreamReader) parseVal(tok json.Token) (*Node, error) {
	switch {
	// Note case order matters, because cur type could be prop|obj or root|obj, in those
	// cases, we want IsJSONObj case to be hit first.
	case IsJSONObj(sp.cur):
		sp.addElementChild(tok.(string), JSONProp)
		sp.streamCandidateCheck()
	// Similarly, we want arr check before prop check.
	case IsJSONArr(sp.cur):
		// if parent is an array or root, so we're adding a value directly to
		// the array or root, by creating an anonymous element node, then the
		// value as text node underneath it.
		sp.addElementChild("", JSONProp)
		sp.streamCandidateCheck()
		sp.addTextChild(tok)
		return sp.wrapUpCurAndTargetCheck()
	case IsJSONProp(sp.cur):
		sp.addTextChild(tok)
		return sp.wrapUpCurAndTargetCheck()
	case IsJSONRoot(sp.cur):
		// A value is directly setting on root. We need to do both stream candidate check
		// and target check.
		sp.streamCandidateCheck()
		sp.addTextChild(tok)
		return sp.wrapUpCurAndTargetCheck()
	}
	return nil, nil
}

func (sp *JSONStreamReader) parse() (*Node, error) {
	for {
		tok, err := sp.d.Token()
		if err != nil {
			// including io.EOF
			return nil, err
		}
		switch tok := tok.(type) {
		case json.Delim:
			ret, err := sp.parseDelim(tok)
			if err != nil {
				return nil, err
			}
			if ret != nil {
				return ret, nil
			}
		case string, float64, bool, nil:
			ret, err := sp.parseVal(tok)
			if err != nil {
				return nil, err
			}
			if ret != nil {
				return ret, nil
			}
		}
	}
}

// Read returns a *Node that matches the xpath streaming criteria.
func (sp *JSONStreamReader) Read() (*Node, error) {
	// Because this is a streaming read, we need to release/remove last
	// stream node from the node tree to free up memory. If Release() is
	// called after Read() call, then sp.stream is already cleaned up;
	// adding this piece of code here just in case Release() isn't called.
	if sp.stream != nil {
		RemoveAndReleaseTree(sp.stream)
		sp.stream = nil
	}
	return sp.parse()
}

// Release releases the *Node (and its subtree) that Read() has previously
// returned. Note even if Release is not explicitly called, next Read() call
// will still release the current streaming target node.
func (sp *JSONStreamReader) Release(n *Node) {
	if n == sp.stream {
		sp.stream = nil
	}
	RemoveAndReleaseTree(n)
}

// AtLine returns the **rough** line number of the current JSON decoder.
func (sp *JSONStreamReader) AtLine() int {
	return sp.r.AtLine()
}

// NewJSONStreamReader creates a new instance of JSON streaming reader. The optional
// ancestorFields declare ancestor fields to be carried onto each streamed target node;
// see AncestorField.
func NewJSONStreamReader(r io.Reader, xpathStr string, ancestorFields ...AncestorField) (*JSONStreamReader, error) {
	xpathStr = strings.TrimSpace(xpathStr)
	xpathNoFilterStr := removeLastFilterInXPath(xpathStr)
	xpathExpr, err := caches.GetXPathExpr(xpathStr)
	if err != nil {
		return nil, fmt.Errorf("invalid xpath '%s', err: %s", xpathStr, err.Error())
	}
	xpathNoFilterExpr, _ := caches.GetXPathExpr(xpathNoFilterStr)
	fields := make([]ancestorField, 0, len(ancestorFields))
	names := make(map[string]bool, len(ancestorFields))
	for _, f := range ancestorFields {
		if strings.TrimSpace(f.Name) == "" {
			return nil, fmt.Errorf("ancestor field name cannot be empty")
		}
		if f.Name == AncestorFieldsNodeName {
			return nil, fmt.Errorf("ancestor field name '%s' is reserved", f.Name)
		}
		if names[f.Name] {
			return nil, fmt.Errorf("ancestor field name '%s' is declared more than once", f.Name)
		}
		names[f.Name] = true
		expr, err := caches.GetXPathExpr(strings.TrimSpace(f.XPath))
		if err != nil {
			return nil, fmt.Errorf(
				"invalid xpath '%s' for ancestor field '%s', err: %s", f.XPath, f.Name, err.Error())
		}
		fields = append(fields, ancestorField{
			name:     f.Name,
			xpathStr: strings.TrimSpace(f.XPath),
			expr:     expr,
			required: f.Required,
		})
	}
	lineCountingReader := ios.NewLineCountingReader(r)
	reader := &JSONStreamReader{
		r:              lineCountingReader,
		d:              json.NewDecoder(lineCountingReader),
		xpathExpr:      xpathNoFilterExpr,
		ancestorFields: fields,
		xpathFilterExpr: func() *xpath.Expr {
			if xpathStr == xpathNoFilterStr {
				return nil
			}
			return xpathExpr
		}(),
		root: CreateJSONNode(DocumentNode, "", JSONRoot),
	}
	reader.cur = reader.root
	return reader, nil
}
