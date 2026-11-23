package json

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/jf-tech/go-corelib/caches"
	"github.com/jf-tech/go-corelib/strs"

	"github.com/jf-tech/omniparser/errs"
	"github.com/jf-tech/omniparser/extensions/omniv21/fileformat"
	"github.com/jf-tech/omniparser/extensions/omniv21/transform"
	"github.com/jf-tech/omniparser/header"
	"github.com/jf-tech/omniparser/idr"
)

const (
	fileFormatJSON = "json"
)

type jsonFileFormat struct {
	schemaName string
}

// NewJSONFileFormat creates a FileFormat for JSON.
func NewJSONFileFormat(schemaName string) fileformat.FileFormat {
	return &jsonFileFormat{schemaName: schemaName}
}

func (f *jsonFileFormat) ValidateSchema(
	format string, schemaContent []byte, finalOutputDecl *transform.Decl) (interface{}, error) {
	if format != fileFormatJSON {
		return nil, errs.ErrSchemaNotSupported
	}
	if finalOutputDecl == nil {
		return nil, f.FmtErr("'FINAL_OUTPUT' is missing")
	}
	xpath := strs.StrPtrOrElse(finalOutputDecl.XPath, ".")
	_, err := caches.GetXPathExpr(xpath)
	if err != nil {
		return nil, f.FmtErr("'FINAL_OUTPUT.xpath' (value: '%s') is invalid, err: %s", xpath, err.Error())
	}
	// Validate the ancestor field declarations now so a malformed declaration is reported as a
	// schema error instead of surfacing later, per input stream, from the reader.
	// 'parser_settings' has been JSON schema validated before schema handlers are created,
	// so the Unmarshal below won't fail.
	var h header.Header
	_ = json.Unmarshal(schemaContent, &h)
	names := make(map[string]bool, len(h.ParserSettings.AncestorFields))
	for _, af := range h.ParserSettings.AncestorFields {
		if strings.TrimSpace(af.Name) == "" {
			return nil, f.FmtErr("'parser_settings.ancestor_fields' has an empty 'name'")
		}
		if af.Name == idr.AncestorFieldsNodeName {
			return nil, f.FmtErr(
				"'parser_settings.ancestor_fields' name '%s' is reserved", af.Name)
		}
		if names[af.Name] {
			return nil, f.FmtErr(
				"'parser_settings.ancestor_fields' name '%s' is declared more than once", af.Name)
		}
		names[af.Name] = true
		if _, err = caches.GetXPathExpr(strings.TrimSpace(af.XPath)); err != nil {
			return nil, f.FmtErr(
				"'parser_settings.ancestor_fields' '%s' has invalid 'xpath' (value: '%s'), err: %s",
				af.Name, af.XPath, err.Error())
		}
	}
	return xpath, nil
}

func (f *jsonFileFormat) CreateFormatReader(
	schemaHeader header.Header,
	name string,
	r io.Reader,
	runtime interface{}) (fileformat.FormatReader, error) {
	// 'parser_settings.ancestor_fields' is file format specific: the JSON streaming
	// reader is the only one capable of carrying ancestor data onto each streamed
	// record (see idr.AncestorField).
	var fields []idr.AncestorField
	for _, af := range schemaHeader.ParserSettings.AncestorFields {
		fields = append(fields, idr.AncestorField{
			Name:     af.Name,
			XPath:    af.XPath,
			Required: af.Required,
		})
	}
	return NewReader(name, r, runtime.(string), fields...)
}

func (f *jsonFileFormat) FmtErr(format string, args ...interface{}) error {
	return fmt.Errorf("schema '%s': %s", f.schemaName, fmt.Sprintf(format, args...))
}
