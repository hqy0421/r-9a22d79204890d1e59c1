# JSON/XML Schema in "Depth" :blush:

Omniparser schemas for JSON and XML inputs contain only two parts, `parser_settings` and
`transform_declarations`, both of which we have covered in depth [here](./gettingstarted.md) and
[here](./transforms.md).

## Ancestor Fields in Streamed JSON Records

JSON and XML inputs are ingested in a streaming fashion: `FINAL_OUTPUT.xpath` tells the reader which
nodes are records, and the reader returns one such node at a time without ever holding the entire
input document (or the entire containing array) in memory. This is what makes it possible to process
export files that are far larger than available memory.

A common structure in such exports is a batch object embedding many line items:

```
{
    "batches": [
        {
            "batch_id": "B1",
            "items": [
                { "sku": "A", "qty": 1 },
                { "sku": "B", "qty": 2 }
            ]
        }
    ]
}
```

Streaming `/batches/*/items/*` gives each item one by one, but the item record by itself doesn't
carry `batch_id`. Since `batch_id` precedes `items`, an xpath such as `../../batch_id` happens to work
from inside the item, but that only holds for as long as `batch_id` remains ahead of `items` in the
document, and it silently yields nothing when the producer moves the field. To make the dependency
explicit, declare the needed ancestor fields in `parser_settings.ancestor_fields` (only
`"file_format_type": "json"` honors this setting):

```
{
    "parser_settings": {
        "version": "omni.2.1",
        "file_format_type": "json",
        "ancestor_fields": [
            { "name": "batch_id", "xpath": "../../batch_id", "required": true },
            { "name": "batch_meta", "xpath": "../../meta" }
        ]
    },
    "transform_declarations": {
        "FINAL_OUTPUT": { "xpath": "/batches/*/items/*", "object": {
            "batch_id": { "xpath": "__ancestors/batch_id" },
            "site": { "xpath": "__ancestors/batch_meta/site" },
            "sku": { "xpath": "sku" },
            "qty": { "xpath": "qty", "type": "int" }
        }}
    }
}
```

Each entry has these fields:

- `name` (required): the name of the field under the `__ancestors` node.
- `xpath` (required): evaluated with the streamed record as the context node **and** as the query
  root, so ancestor data must be addressed with a relative path such as `../..`. A
  document-absolute path (leading `/`) starts at the streamed record, not at the input document, and
  therefore can't reach an ancestor. The path must resolve to at most one node.
- `required` (optional, default `false`): makes a missing ancestor field a fatal error for the
  record.

For every streamed record the reader evaluates each declaration and copies the resolved node (its
whole subtree if it is an object or an array) underneath a reserved `__ancestors` node appended to
the record's IDR tree. The output above therefore contains `"batch_id": "B1"` for each item as long
as `batch_id` has already been decoded when the item completes - see the contract below for what
happens otherwise.

### Contract when ancestor fields move or are missing

Because the input is only ever read forward, once an item has been returned nothing that follows it
in the document can be attached to it. This is the explicit contract for the declared fields:

| Situation | Behavior |
| --- | --- |
| `xpath` matches an ancestor field already decoded before the record completes | The value is copied onto the record under `__ancestors/<name>`. |
| `xpath` matches nothing, or the field exists but appears **after** the record in the document | `required: false` (default): the field is omitted from `__ancestors` (and `__ancestors` itself is omitted when no field is found); `required: true`: the reader fails the input with `required ancestor field '<name>' (xpath: '<xpath>') not found or not yet decoded`. |
| `xpath` matches more than one node | Fatal reader error `ancestor field '<name>' (xpath: '<xpath>') resolves to more than one node`. |
| `xpath` matches a node inside the streamed record | Fatal reader error `ancestor field '<name>' (xpath: '<xpath>') resolves inside the streamed record` - the record is released after each `Read()` so it can't host its own ancestor data. |
| The streamed record isn't a JSON object | Fatal reader error `ancestor fields cannot be attached to non-object record '<json>'`. |
| The streamed record already has a property named `__ancestors` | Fatal reader error `streamed record '<json>' already contains reserved property '__ancestors'`. |

Setting `required: true` turns a schema that depended on field ordering into a loud failure if a
producer moves (or drops) the ancestor field after the target, rather than emitting records that
silently lack the batch number. Conversely, a non-required field may be used for data that is
legitimately optional.

Notes:

- `ancestor_fields` is validated with the schema: a blank `name` or an invalid `xpath` fails schema
  loading, not ingestion.
- `json` is the only file format that honors `ancestor_fields`; for other formats it is ignored.
- The reserved node name `__ancestors` must not be used by a property of a streamed record.
- Ancestor fields are resolved against the partially built ancestor tree, which is exactly what has
  been decoded so far. Only declare fields whose value is available before the record completes;
  `required: true` reports the violation for you.
- The resolved node's subtree is copied, so declare the scalar or small object fields you need rather
  than an ancestor that contains the streamed array itself (for instance prefer `../../batch_id` over
  `../..`), otherwise the copy defeats the point of streaming and can grow with the input.
