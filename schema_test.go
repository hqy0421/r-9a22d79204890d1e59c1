package omniparser

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jf-tech/go-corelib/testlib"
	"github.com/stretchr/testify/assert"

	"github.com/jf-tech/omniparser/customfuncs"
	"github.com/jf-tech/omniparser/errs"
	"github.com/jf-tech/omniparser/header"
	"github.com/jf-tech/omniparser/schemahandler"
	"github.com/jf-tech/omniparser/transformctx"
)

func TestNewSchema(t *testing.T) {
	for _, test := range []struct {
		name   string
		schema string
		exts   []Extension
		err    string
	}{
		{
			name:   "fail to read out schema content",
			schema: "",
			exts:   nil,
			err:    "unable to read schema 'test-schema': mock reading failure",
		},
		{
			name:   "fail to unmarshal schema header",
			schema: "[invalid",
			exts:   nil,
			err:    "unable to perform schema validation: invalid character 'i' looking for beginning of value",
		},
		{
			name:   "json schema validation for header failed",
			schema: `{"parser_settings": {"versionx": "9999", "file_format_type": "exe" }}`,
			exts:   nil,
			err:    "schema 'test-schema' validation failed:\nparser_settings: Additional property versionx is not allowed\nparser_settings: version is required",
		},
		{
			name:   "no supported schema handler",
			schema: `{"parser_settings": {"version": "9999", "file_format_type": "exe" }}`,
			exts: []Extension{
				{}, // Empty to test if we skip empty 3rd party handler config properly or not.
				{
					CreateSchemaHandler: func(_ *schemahandler.CreateCtx) (schemahandler.SchemaHandler, error) {
						return nil, errs.ErrSchemaNotSupported
					},
				},
			},
			err: errs.ErrSchemaNotSupported.Error(),
		},
		{
			name:   "supported schema handler found, but schema validation failed",
			schema: `{"parser_settings": {"version": "9999", "file_format_type": "exe" }}`,
			exts: []Extension{
				{
					CreateSchemaHandler: func(_ *schemahandler.CreateCtx) (schemahandler.SchemaHandler, error) {
						return nil, errors.New("invalid schema")
					},
				},
			},
			err: "invalid schema",
		},
		{
			name:   "supported schema handler found, schema parsing successful",
			schema: `{"parser_settings": {"version": "9999", "file_format_type": "exe" }}`,
			exts: []Extension{
				{
					CustomFuncs: customfuncs.CustomFuncs{
						"upper": func() {},
						"lower": func() {},
					},
					CreateSchemaHandler: func(ctx *schemahandler.CreateCtx) (schemahandler.SchemaHandler, error) {
						assert.Equal(t, 2, len(ctx.CustomFuncs))
						assert.NotNil(t, ctx.CustomFuncs["upper"])
						assert.NotNil(t, ctx.CustomFuncs["lower"])
						// make sure create params are passed in correctly.
						assert.Equal(t, 13, ctx.CreateParams.(int))
						return nil, nil
					},
					CreateSchemaHandlerParams: 13,
				},
			},
			err: "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var schemaReader io.Reader
			if test.schema == "" {
				schemaReader = testlib.NewMockReadCloser("mock reading failure", nil)
			} else {
				schemaReader = strings.NewReader(test.schema)
			}
			schema, err := NewSchema("test-schema", schemaReader, test.exts...)
			if test.err != "" {
				assert.Error(t, err)
				assert.Equal(t, test.err, err.Error())
				assert.Nil(t, schema)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, schema)
				assert.Equal(t, test.schema, string(schema.Content()))
			}
		})
	}
}

func TestSchema_NewTransform_StripBOMFailure(t *testing.T) {
	s := &schema{
		header: header.Header{
			ParserSettings: header.ParserSettings{Version: "999", FileFormatType: "exe"},
		},
	}
	op, err := s.NewTransform("test input", testlib.NewMockReadCloser("bom read failure", nil), nil)
	assert.Error(t, err)
	assert.Equal(t, "bom read failure", err.Error())
	assert.Nil(t, op)
}

type testSchemaHandler struct {
	newIngesterErr error
}

func (t testSchemaHandler) NewIngester(_ *transformctx.Ctx, _ io.Reader) (schemahandler.Ingester, error) {
	if t.newIngesterErr != nil {
		return nil, t.newIngesterErr
	}
	return &testIngester{}, nil
}

func TestSchema_NewTransform_NewIngesterFailure(t *testing.T) {
	p := &schema{
		header: header.Header{
			ParserSettings: header.ParserSettings{Version: "999", FileFormatType: "exe"},
		},
		handler: testSchemaHandler{newIngesterErr: errors.New("test failure")},
	}
	transform, err := p.NewTransform("test input", strings.NewReader("something"), &transformctx.Ctx{})
	assert.Error(t, err)
	assert.Equal(t, "test failure", err.Error())
	assert.Nil(t, transform)
}

func TestSchema_NewTransform_NameAndCtxAwareErrOverwrite(t *testing.T) {
	h := header.Header{
		ParserSettings: header.ParserSettings{Version: "999", FileFormatType: "exe"},
	}
	s := &schema{
		header:  h,
		content: []byte("test schema content"),
		handler: testSchemaHandler{},
	}
	ctx := &transformctx.Ctx{}
	transform, err := s.NewTransform("test input", strings.NewReader("something"), ctx)
	assert.NoError(t, err)
	assert.NotNil(t, transform)
	assert.Equal(t, "test input", ctx.InputName)
	assert.NotNil(t, ctx.CtxAwareErr)

	assert.Equal(t, h, s.Header())
	assert.Equal(t, "test schema content", string(s.Content()))
}

func TestSchema_AncestorFields_EndToEnd(t *testing.T) {
	schema := []byte(`{
		"parser_settings": {
			"version": "omni.2.1",
			"file_format_type": "json",
			"ancestor_fields": [
				{ "name": "batch_id", "xpath": "../../batch_id", "required": true }
			]
		},
		"transform_declarations": {
			"FINAL_OUTPUT": { "xpath": "/batches/*/items/*", "object": {
				"batch_id": { "xpath": "__ancestors/batch_id" },
				"sku": { "xpath": "sku" },
				"qty": { "xpath": "qty", "type": "int" }
			}}
		}
	}`)
	input := `{"batches":[
		{"batch_id":"B1","items":[{"sku":"A","qty":1},{"sku":"B","qty":2}]},
		{"batch_id":"B2","items":[{"sku":"C","qty":3}]}]}`
	s, err := NewSchema("test-schema", bytes.NewReader(schema))
	assert.NoError(t, err)
	tr, err := s.NewTransform("test-input", bytes.NewReader([]byte(input)), &transformctx.Ctx{})
	assert.NoError(t, err)
	var got []string
	for {
		b, err := tr.Read()
		if err == io.EOF {
			break
		}
		assert.NoError(t, err)
		got = append(got, string(b))
	}
	assert.Equal(t, []string{
		`{"batch_id":"B1","qty":1,"sku":"A"}`,
		`{"batch_id":"B1","qty":2,"sku":"B"}`,
		`{"batch_id":"B2","qty":3,"sku":"C"}`,
	}, got)
}

func TestSchema_AncestorFields_RequiredFieldNotYetDecoded(t *testing.T) {
	schema := []byte(`{
		"parser_settings": {
			"version": "omni.2.1",
			"file_format_type": "json",
			"ancestor_fields": [
				{ "name": "batch_id", "xpath": "../../batch_id", "required": true }
			]
		},
		"transform_declarations": {
			"FINAL_OUTPUT": { "xpath": "/batches/*/items/*", "object": {
				"batch_id": { "xpath": "__ancestors/batch_id" }
			}}
		}
	}`)
	// 'batch_id' comes after 'items', so it can't be carried onto the items.
	input := `{"batches":[{"items":[{"sku":"A"}],"batch_id":"B1"}]}`
	s, err := NewSchema("test-schema", bytes.NewReader(schema))
	assert.NoError(t, err)
	tr, err := s.NewTransform("test-input", bytes.NewReader([]byte(input)), &transformctx.Ctx{})
	assert.NoError(t, err)
	_, err = tr.Read()
	assert.Error(t, err)
	assert.Equal(t,
		`input 'test-input' before/near line 1: required ancestor field 'batch_id' (xpath: '../../batch_id') not found or not yet decoded`,
		err.Error())
}
