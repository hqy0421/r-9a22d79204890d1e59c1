package idr

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJSONStreamReader(t *testing.T) {
	for _, test := range []struct {
		name     string
		js       string
		xpath    string
		err      string
		expected []string
	}{
		{
			name:     "invalid xpath",
			js:       ``,
			xpath:    "[invalid",
			err:      `invalid xpath '[invalid', err: expression must evaluate to a node-set`,
			expected: nil,
		},
		{
			name:     "str on root",
			js:       `"test"`,
			xpath:    ".",
			err:      ``,
			expected: []string{`"test"`},
		},
		{
			name:     "num on root",
			js:       `3.1415`,
			xpath:    ".",
			err:      ``,
			expected: []string{`3.1415`},
		},
		{
			name:     "boolean on root",
			js:       `true`,
			xpath:    ".",
			err:      ``,
			expected: []string{`true`},
		},
		{
			name:     "null on root",
			js:       `null`,
			xpath:    ".",
			err:      ``,
			expected: []string{`null`},
		},
		{
			name:     "empty obj on root",
			js:       `{}`,
			xpath:    "/",
			err:      ``,
			expected: []string{`{}`},
		},
		{
			name:     "non-empty obj on root",
			js:       `{"a":1, "b":true, "c":[], "d":{}, "e":null}`,
			xpath:    ".",
			err:      ``,
			expected: []string{`{"a":1,"b":true,"c":[],"d":{},"e":null}`},
		},
		{
			name:     "empty arr on root",
			js:       `[]`,
			xpath:    ".",
			err:      ``,
			expected: []string{`[]`},
		},
		{
			name:     "non-empty arr on root",
			js:       `["1", 3.14, true, [], null, {"a":"b"}]`,
			xpath:    ".",
			err:      ``,
			expected: []string{`["1",3.14,true,[],null,{"a":"b"}]`},
		},
		{
			name:     "non-trivial xpath on root obj",
			js:       `{"a":1, "b":true, "c":[], "d":{}, "e":null}`,
			xpath:    "/a",
			err:      ``,
			expected: []string{`1`},
		},
		{
			name:     "non-trivial xpath arr on root",
			js:       `["1", 3.14, true, [], null, {"a":"b"}]`,
			xpath:    "/*",
			err:      ``,
			expected: []string{`"1"`, `3.14`, `true`, `[]`, `null`, `{"a":"b"}`},
		},
		{
			name:     "xpath and filter both effective",
			js:       `{"a":1, "b":true, "c":[], "d":{}, "e":null}`,
			xpath:    "/*[. != '']",
			err:      ``,
			expected: []string{`1`, `true`},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sp, err := NewJSONStreamReader(strings.NewReader(test.js+"\n"), test.xpath)
			if test.err != "" {
				assert.Error(t, err)
				assert.Equal(t, test.err, err.Error())
				assert.Nil(t, sp)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, 1, sp.AtLine())
				for {
					n, err := sp.Read()
					if err == io.EOF {
						assert.Equal(t, 0, len(test.expected))
						break
					}
					assert.NoError(t, err)
					assert.True(t, len(test.expected) > 0)
					assert.Equal(t, test.expected[0], JSONify2(n))
					sp.Release(n)
					test.expected = test.expected[1:]
				}
				assert.Equal(t, 2, sp.AtLine())
			}
		})
	}
}

func TestJSONStreamReader_AncestorFields(t *testing.T) {
	js := `{
		"batches": [
			{
				"batch_id": "B1",
				"batch_meta": { "site": "s1" },
				"items": [
					{ "sku": "A", "qty": 1 },
					{ "sku": "B", "qty": 2 }
				]
			},
			{
				"batch_id": "B2",
				"batch_meta": { "site": "s2" },
				"items": [
					{ "sku": "C", "qty": 3 }
				]
			}
		]
	}`
	for _, test := range []struct {
		name     string
		xpath    string
		fields   []AncestorField
		err      string
		expected []string
	}{
		{
			name:  "ancestor field preceding the target",
			xpath: "/batches/*/items/*",
			fields: []AncestorField{
				{Name: "batch_id", XPath: "../../batch_id"},
			},
			expected: []string{
				`{"__ancestors":{"batch_id":"B1"},"qty":1,"sku":"A"}`,
				`{"__ancestors":{"batch_id":"B1"},"qty":2,"sku":"B"}`,
				`{"__ancestors":{"batch_id":"B2"},"qty":3,"sku":"C"}`,
			},
		},
		{
			name:  "multiple ancestor fields, object ancestor field subtree copied",
			xpath: "/batches/*/items/*",
			fields: []AncestorField{
				{Name: "batch_id", XPath: "../../batch_id"},
				{Name: "meta", XPath: "../../batch_meta"},
				{Name: "batch_index", XPath: "../../batch_id"},
			},
			expected: []string{
				`{"__ancestors":{"batch_id":"B1","batch_index":"B1","meta":{"site":"s1"}},"qty":1,"sku":"A"}`,
				`{"__ancestors":{"batch_id":"B1","batch_index":"B1","meta":{"site":"s1"}},"qty":2,"sku":"B"}`,
				`{"__ancestors":{"batch_id":"B2","batch_index":"B2","meta":{"site":"s2"}},"qty":3,"sku":"C"}`,
			},
		},
		{
			name:  "optional ancestor field not present is skipped",
			xpath: "/batches/*/items/*",
			fields: []AncestorField{
				{Name: "missing", XPath: "../../no_such_field"},
			},
			expected: []string{
				`{"qty":1,"sku":"A"}`,
				`{"qty":2,"sku":"B"}`,
				`{"qty":3,"sku":"C"}`,
			},
		},
		{
			name:  "required ancestor field not present",
			xpath: "/batches/*/items/*",
			fields: []AncestorField{
				{Name: "missing", XPath: "../../no_such_field", Required: true},
			},
			err: `required ancestor field 'missing' (xpath: '../../no_such_field') not found or not yet decoded`,
		},
		{
			name:  "ancestor field resolving to more than one node",
			xpath: "/batches/*/items/*",
			fields: []AncestorField{
				{Name: "many", XPath: "../../*"},
			},
			err: `ancestor field 'many' (xpath: '../../*') resolves to more than one node`,
		},
		{
			name:  "ancestor field resolving inside the target",
			xpath: "/batches/*/items/*",
			fields: []AncestorField{
				{Name: "sku", XPath: "sku"},
			},
			err: `ancestor field 'sku' (xpath: 'sku') resolves inside the streamed record`,
		},
		{
			name:   "no ancestor field configured behaves as before",
			xpath:  "/batches/*/items/*",
			fields: nil,
			expected: []string{
				`{"qty":1,"sku":"A"}`,
				`{"qty":2,"sku":"B"}`,
				`{"qty":3,"sku":"C"}`,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sp, err := NewJSONStreamReader(strings.NewReader(js+"\n"), test.xpath, test.fields...)
			assert.NoError(t, err)
			for {
				n, err := sp.Read()
				if test.err != "" {
					assert.Error(t, err)
					assert.Equal(t, test.err, err.Error())
					assert.Nil(t, n)
					return
				}
				if err == io.EOF {
					assert.Equal(t, 0, len(test.expected))
					break
				}
				assert.NoError(t, err)
				assert.True(t, len(test.expected) > 0)
				assert.Equal(t, test.expected[0], JSONify2(n))
				sp.Release(n)
				test.expected = test.expected[1:]
			}
		})
	}
}

func TestJSONStreamReader_AncestorFields_NotYetDecoded(t *testing.T) {
	// 'batch_id' appears after 'items', so by the time each item is streamed the batch_id
	// hasn't been decoded yet and isn't visible to the streaming reader.
	js := `{"batches": [{"items": [{"sku": "A"}], "batch_id": "B1"}]}`
	sp, err := NewJSONStreamReader(
		strings.NewReader(js+"\n"), "/batches/*/items/*",
		AncestorField{Name: "batch_id", XPath: "../../batch_id"})
	assert.NoError(t, err)
	n, err := sp.Read()
	assert.NoError(t, err)
	assert.Equal(t, `{"sku":"A"}`, JSONify2(n))
	sp.Release(n)

	// Now with Required, the same input is a fatal error.
	sp, err = NewJSONStreamReader(
		strings.NewReader(js+"\n"), "/batches/*/items/*",
		AncestorField{Name: "batch_id", XPath: "../../batch_id", Required: true})
	assert.NoError(t, err)
	n, err = sp.Read()
	assert.Error(t, err)
	assert.Equal(t,
		`required ancestor field 'batch_id' (xpath: '../../batch_id') not found or not yet decoded`,
		err.Error())
	assert.Nil(t, n)
}

func TestJSONStreamReader_AncestorFields_InvalidConfig(t *testing.T) {
	for _, test := range []struct {
		name   string
		fields []AncestorField
		err    string
	}{
		{
			name:   "empty name",
			fields: []AncestorField{{Name: " ", XPath: "../a"}},
			err:    `ancestor field name cannot be empty`,
		},
		{
			name:   "reserved name",
			fields: []AncestorField{{Name: AncestorFieldsNodeName, XPath: "../a"}},
			err:    `ancestor field name '__ancestors' is reserved`,
		},
		{
			name:   "duplicate name",
			fields: []AncestorField{{Name: "a", XPath: "../a"}, {Name: "a", XPath: "../b"}},
			err:    `ancestor field name 'a' is declared more than once`,
		},
		{
			name:   "invalid xpath",
			fields: []AncestorField{{Name: "a", XPath: "[invalid"}},
			err:    `invalid xpath '[invalid' for ancestor field 'a', err: expression must evaluate to a node-set`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sp, err := NewJSONStreamReader(strings.NewReader("{}"), ".", test.fields...)
			assert.Error(t, err)
			assert.Equal(t, test.err, err.Error())
			assert.Nil(t, sp)
		})
	}
}

func TestJSONStreamReader_AncestorFields_ScalarTarget(t *testing.T) {
	sp, err := NewJSONStreamReader(
		strings.NewReader(`{"a": 1, "b": 2}`), "/*",
		AncestorField{Name: "root", XPath: ".."})
	assert.NoError(t, err)
	n, err := sp.Read()
	assert.Error(t, err)
	assert.Equal(t, `ancestor fields cannot be attached to non-object record '1'`, err.Error())
	assert.Nil(t, n)
}

func TestJSONStreamReader_AncestorFields_ReservedPropertyConflict(t *testing.T) {
	sp, err := NewJSONStreamReader(
		strings.NewReader(`{"items":[{"sku":"A","__ancestors":{"x":"y"}}]}`), "/items/*",
		AncestorField{Name: "root", XPath: ".."})
	assert.NoError(t, err)
	n, err := sp.Read()
	assert.Error(t, err)
	assert.Equal(t,
		`streamed record '{"__ancestors":{"x":"y"},"sku":"A"}' already contains reserved property '__ancestors'`,
		err.Error())
	assert.Nil(t, n)
}

func TestJSONStreamReader_AncestorFields_WithFilter(t *testing.T) {
	js := `{"batches":[{"batch_id":"B1","items":[{"sku":"A"},{"sku":"B"},{"sku":"C"}]}]}`
	sp, err := NewJSONStreamReader(
		strings.NewReader(js+"\n"), "/batches/*/items/*[sku='B']",
		AncestorField{Name: "batch_id", XPath: "../../batch_id", Required: true})
	assert.NoError(t, err)
	n, err := sp.Read()
	assert.NoError(t, err)
	assert.Equal(t, `{"__ancestors":{"batch_id":"B1"},"sku":"B"}`, JSONify2(n))
	sp.Release(n)
	_, err = sp.Read()
	assert.Equal(t, io.EOF, err)
}
