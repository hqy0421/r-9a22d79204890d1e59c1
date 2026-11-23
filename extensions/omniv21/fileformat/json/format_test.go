package json

import (
	"io"
	"strings"
	"testing"

	"github.com/bradleyjkemp/cupaloy"
	"github.com/jf-tech/go-corelib/strs"
	"github.com/stretchr/testify/assert"

	"github.com/jf-tech/omniparser/errs"
	"github.com/jf-tech/omniparser/extensions/omniv21/transform"
	"github.com/jf-tech/omniparser/header"
	"github.com/jf-tech/omniparser/idr"
)

func TestValidateSchema(t *testing.T) {
	for _, test := range []struct {
		name        string
		format      string
		decl        *transform.Decl
		expected    interface{}
		expectedErr string
	}{
		{
			name:        "not supported format",
			format:      "exe",
			decl:        nil,
			expected:    nil,
			expectedErr: errs.ErrSchemaNotSupported.Error(),
		},
		{
			name:        "FINAL_OUTPUT decl is nil",
			format:      fileFormatJSON,
			decl:        nil,
			expected:    nil,
			expectedErr: `schema 'test-schema': 'FINAL_OUTPUT' is missing`,
		},
		{
			name:        "FINAL_OUTPUT 'xpath' is invalid",
			format:      fileFormatJSON,
			decl:        &transform.Decl{XPath: strs.StrPtr("[invalid")},
			expected:    nil,
			expectedErr: `schema 'test-schema': 'FINAL_OUTPUT.xpath' (value: '[invalid') is invalid, err: expression must evaluate to a node-set`,
		},
		{
			name:        "success 1",
			format:      fileFormatJSON,
			decl:        &transform.Decl{XPath: strs.StrPtr("/A/B[.!='skip']")},
			expected:    "/A/B[.!='skip']",
			expectedErr: "",
		},
		{
			name:        "success 2",
			format:      fileFormatJSON,
			decl:        &transform.Decl{},
			expected:    ".",
			expectedErr: "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, err := NewJSONFileFormat("test-schema").ValidateSchema(test.format, nil, test.decl)
			if test.expectedErr != "" {
				assert.Error(t, err)
				assert.Equal(t, test.expectedErr, err.Error())
				assert.Nil(t, runtime)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, test.expected, runtime)
			}
		})
	}
}

func TestCreateFormatReader(t *testing.T) {
	r, err := NewJSONFileFormat("test-schema").CreateFormatReader(
		header.Header{},
		"test-input",
		strings.NewReader(`["B1", "B2", "B3"]`),
		"/*[.!='B2']")
	assert.NoError(t, err)
	assert.NotNil(t, r)
	t.Run("B1", func(t *testing.T) {
		n1, err := r.Read()
		assert.NoError(t, err)
		cupaloy.SnapshotT(t, idr.JSONify1(n1))
	})
	t.Run("B3", func(t *testing.T) {
		n2, err := r.Read()
		assert.NoError(t, err)
		cupaloy.SnapshotT(t, idr.JSONify1(n2))
	})
	t.Run("EOF", func(t *testing.T) {
		n3, err := r.Read()
		assert.Error(t, err)
		assert.Equal(t, io.EOF, err)
		assert.Nil(t, n3)
	})

	r, err = NewJSONFileFormat("test-schema").CreateFormatReader(
		header.Header{}, "test-input", strings.NewReader(""), "[invalid")
	assert.Error(t, err)
	assert.Equal(t, `invalid xpath '[invalid', err: expression must evaluate to a node-set`, err.Error())
	assert.Nil(t, r)
}

func TestValidateSchema_AncestorFields(t *testing.T) {
	for _, test := range []struct {
		name        string
		schema      string
		expectedErr string
	}{
		{
			name: "valid ancestor fields",
			schema: `{
				"parser_settings": {
					"version": "omni.2.1",
					"file_format_type": "json",
					"ancestor_fields": [
						{ "name": "batch_id", "xpath": "../../batch_id", "required": true }
					]
				}
			}`,
		},
		{
			name: "empty ancestor field name",
			schema: `{
				"parser_settings": {
					"version": "omni.2.1",
					"file_format_type": "json",
					"ancestor_fields": [ { "name": " ", "xpath": "../a" } ]
				}
			}`,
			expectedErr: `schema 'test-schema': 'parser_settings.ancestor_fields' has an empty 'name'`,
		},
		{
			name: "reserved ancestor field name",
			schema: `{
				"parser_settings": {
					"version": "omni.2.1",
					"file_format_type": "json",
					"ancestor_fields": [ { "name": "__ancestors", "xpath": "../a" } ]
				}
			}`,
			expectedErr: `schema 'test-schema': 'parser_settings.ancestor_fields' name '__ancestors' is reserved`,
		},
		{
			name: "duplicate ancestor field name",
			schema: `{
				"parser_settings": {
					"version": "omni.2.1",
					"file_format_type": "json",
					"ancestor_fields": [
						{ "name": "a", "xpath": "../a" },
						{ "name": "a", "xpath": "../b" }
					]
				}
			}`,
			expectedErr: `schema 'test-schema': 'parser_settings.ancestor_fields' name 'a' is declared more than once`,
		},
		{
			name: "invalid ancestor field xpath",
			schema: `{
				"parser_settings": {
					"version": "omni.2.1",
					"file_format_type": "json",
					"ancestor_fields": [ { "name": "a", "xpath": "[invalid" } ]
				}
			}`,
			expectedErr: `schema 'test-schema': 'parser_settings.ancestor_fields' 'a' has invalid 'xpath' (value: '[invalid'), err: expression must evaluate to a node-set`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, err := NewJSONFileFormat("test-schema").ValidateSchema(
				fileFormatJSON, []byte(test.schema), &transform.Decl{XPath: strs.StrPtr("/A/*")})
			if test.expectedErr != "" {
				assert.Error(t, err)
				assert.Equal(t, test.expectedErr, err.Error())
				assert.Nil(t, runtime)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, "/A/*", runtime)
			}
		})
	}
}

func TestCreateFormatReader_AncestorFields(t *testing.T) {
	r, err := NewJSONFileFormat("test-schema").CreateFormatReader(
		header.Header{ParserSettings: header.ParserSettings{
			AncestorFields: []header.AncestorField{
				{Name: "batch_id", XPath: "../../batch_id"},
			},
		}},
		"test-input",
		strings.NewReader(`{"batches":[{"batch_id":"B1","items":[{"sku":"A"},{"sku":"B"}]}]}`),
		"/batches/*/items/*")
	assert.NoError(t, err)
	assert.NotNil(t, r)
	for _, expected := range []string{
		`{"__ancestors":{"batch_id":"B1"},"sku":"A"}`,
		`{"__ancestors":{"batch_id":"B1"},"sku":"B"}`,
	} {
		n, err := r.Read()
		assert.NoError(t, err)
		assert.Equal(t, expected, idr.JSONify2(n))
		r.Release(n)
	}
	_, err = r.Read()
	assert.Equal(t, io.EOF, err)
}
