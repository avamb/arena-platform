# Hall model `seating-json-v2`, issues, operations and the service contract

This file is the design of wave 2 of spec `08_architecture/64_venue_plan_intake_and_verification_ru.md`
(revision 2, sections 4.5, 4.9, 4.10) on the generator side. Code and tests must agree with it.
If the code must differ, change this file in the same commit.

## 1. Why a new version and not a strict v1

Three producers wrote three different documents under the name `seating-json-v1`:

| Producer | Seat fields | Rows | Status |
|---|---|---|---|
| `xlsx_seating_adapter` | `seat_number` int, `number_status`, `confidence` float, `computed_geometry.grid_x/y`, `section_label`, `row_label` | nested `rows[].seats[]` and flat `seats[]` | `validation.status` PASS or QUARANTINE, `issues` are strings |
| `pdf_seating_adapter` | `seat_number` int, `confidence` object (`geometry`, `number`, `identity`), `quarantine_reasons[]`, `row_label` always null | flat `seats[]`, `rows[]` are geometric bands | always QUARANTINE |
| `raster_seating_adapter` | `seat_number` null, `source_bbox`, `confidence` object | flat `seats[]`, bands | always QUARANTINE, `validation.reasons` |
| `venue_generator.table_pipeline` | `seat_number` str or int, `provenance`, `section_id` holds the LABEL | flat `seats[]` only | `SOURCE_COVERAGE_PASS` or `INCOMPLETE` |

A strict v1 schema would make most existing v1 documents invalid, and the frozen M1 regression
results are v1. So v1 stays as the private output format of each adapter, and the service speaks one
new document, `seating-json-v2`. Converters live in `hall_model/convert.py`.

## 2. The document

Schema file: `hall_model/seating-json-v2.schema.json` (JSON Schema draft 2020-12, `additionalProperties: false`
everywhere except `evidence`).

```jsonc
{
  "schema_version": "seating-json-v2",
  "lane": "exact",                 // "exact": geometry from a source. "schematic": not to scale (spec 4.9)
  "confirmed": false,              // the CALLER sets true after the organizer confirmed a schematic hall
  "source_only": true,
  "reference_svg_used": false,
  "sources": [                     // may be empty (schematic lane, model made by operations)
    {"index": 0, "kind": "xlsx", "sha256": "…", "size_bytes": 1234,
     "adapter": "xlsx_seating_adapter", "adapter_version": "0.1.0", "adapter_profile": "auto-edge-markers-v1"}
  ],
  "coordinate_space": {            // how `position` relates to the source, for crops
    "units": "seat_pitch",         // always: 1.0 is the distance between two adjacent seats
    "source_units": "grid",        // "grid" | "pt" | "px" | "none"
    "scale": 1.0, "offset_x": 0.0, "offset_y": 0.0   // position = source * scale + offset
  },
  "stage": {"side": "unknown", "confidence": 0.0},   // "top" | "bottom" | "left" | "right" | "unknown"
  "sections": [
    {
      "id": "s1", "label": "PARTERRE", "label_status": "read",
      "rows": [
        {
          "id": "s1-r1", "label": "1", "label_status": "read", "confidence": 1.0,
          "numbering": {"rule": "explicit", "start": null},
          "seats": [
            {"key": "xlsx-seat-Seating-B2", "number": "1", "number_status": "read",
             "status": "available", "unavailable_reason": null,
             "confidence": {"geometry": 1.0, "number": 1.0, "identity": 1.0},
             "position": {"x": 2.0, "y": 2.0},
             "evidence": {"...": "free form, copied from the adapter, no host path, no file name"}}
          ],
          "gaps": [{"after_seat_key": "xlsx-seat-Seating-D2", "width": 1}]
        }
      ]
    }
  ],
  "issues": [],
  "validation": {"status": "PASS", "seat_count": 24, "sellable_seat_count": 24,
                 "blocking_issue_count": 0, "open_issue_count": 0}
}
```

Rules:

- `id` of a section and of a row is unique in the whole document. `key` of a seat is unique in the
  whole document. Ids and keys are opaque strings (1..120 chars).
- `label` is a string or null. `label_status`: `read` (from the source), `observed` (a model or a
  person read it, not yet reviewed), `confirmed` (set by an operation), `unknown` (label is null).
- `number` is a string or null. `number_status`: `read`, `observed`, `rule` (written by a numbering
  rule that an operation or a structure gave), `confirmed`, `unknown` (number is null).
- An adapter never writes `rule`. A seat number is never derived from a position by an adapter.
  Only a `set_numbering` operation, `add_row`, `add_section`, `set_row_seat_count` on a row with a
  known rule, and the structural lane (spec 4.9, the rule came from outside) write `rule`.
- `status`: `available`, `unavailable` (exists, not for sale, `unavailable_reason` says why).
- `numbering.rule`: `left_to_right`, `right_to_left`, `from_center`, `explicit` (numbers were read
  one by one), `unknown`. `from_center`: the numbers grow from the centre outwards, odd numbers go
  to the left, even numbers to the right; with an even seat count the left centre seat is `start`.
- Seats of a row are stored left to right as the viewer looks at the drawing. `position` is in
  seat-pitch units. The authoring SVG is NOT in these units: it is in pixel-like user units (section 4),
  `svg = position * info.scale + info.offset`.
- `gaps[].after_seat_key` names the seat on the left of the aisle. `width` is in seats.
- Limits (the schema and the validator enforce them): 60 sections, 400 rows, 600 seats in a row,
  20000 seats, 2000 issues, 20 sources.

## 3. Issues with an address

```jsonc
{
  "id": "numbering_unknown:s1-r4",   // stable for the same model state
  "type": "numbering_unknown",        // see the table
  "origin": "derived",                // "derived": the validator computes it. "reported": an adapter or the caller wrote it
  "blocking": true,
  "resolved": false,                  // the caller sets true on a reported issue after the answer
  "codes": ["seat_number_absent"],    // the original adapter codes, for the training log
  "message": "Row 4 has 3 seats without a number.",   // English, STE; the UI text is built by arena
  "target": {
    "section_id": "s1", "row_id": "s1-r4", "seat_keys": ["…"],
    "bbox": {"space": "source", "source_index": 0, "page": 1, "x0": 10, "y0": 20, "x1": 300, "y1": 44}
  }
}
```

`target.bbox` is in SOURCE units (PDF points, image pixels, worksheet cells) so the worker can cut a
crop of the source. It is null when there is no source. `section_id`, `row_id` may be null, `seat_keys`
may be empty.

| Type | Blocking by default | Derived by the validator when | Adapter codes mapped to it |
|---|---|---|---|
| `numbering_unknown` | yes | an available seat has `number` null | `unknown_numbers`, `unknown_numeric_candidate`, `seat_number_absent`, `multiple_numbers_in_rectangle`, `seat_number_not_single_positive_integer`, `number_unknown`, `missing_or_ambiguous_row_glyph` is NOT here |
| `row_label_unknown` | yes | a row with seats has `label` null | `official_row_unverified`, `row_marker_unknown`, `no_single_letter_row_marker`, `insufficient_numeric_edge_markers`, `edge_numeric_markers_disagree`, `missing_or_ambiguous_row_glyph` |
| `row_count_mismatch` | yes | never derived; reported by the structural lane (low confidence of a seat count) and by `number_without_closed_cell`, `no_source_ink_for_annotated_label` | |
| `unavailable_seats` | no | never derived; reported when a source marks seats as not for sale | |
| `section_split` | no | never derived; reported by `official_section_unverified`, `conflicting_section_labels` | |
| `stage_unknown` | no | `stage.side` is `unknown` | |
| `unknown` | yes | duplicate identity (section label, row label, number), duplicate key, a seat without a position in the exact lane, and observed-not-reviewed labels or numbers (`raster_candidate_unreviewed`, `ai_observation_pending_review`) | every other adapter code, kept in `codes` |

`validation.status` is `PASS` only when there is no unresolved blocking issue and
(`lane` is `exact` or `confirmed` is true). Otherwise `QUARANTINE`. The validator
(`hall_model.validate.revalidate(model)`) drops old derived issues, keeps reported ones, computes the
derived ones again and writes `validation`. Every service route runs it before it answers.

A seat is sellable when: `status` is `available`, `number` is not null, its row has a label, its
identity is unique, and `number_status` and the row `label_status` are not `observed`.
An empty string is a schema error; a label or number of spaces only counts as null. Identity is
(section label or `Main`, row label, number) with the spaces at the ends removed, so a section without a
label and a section `Main` are one section for identity.

Additions made during the build: a section may carry `placement: {band, order}` (schematic lane), the top
level may carry `geometry_edited`, `coordinate_space` may carry `page_offsets` (PDF pages are stacked
vertically), and a seat `evidence` carries `source_bbox`, `page` and `quarantine_reasons` from the adapter.

## 4. Authoring SVG and preview PNG

- `arena_export.exporter_v2.build_authoring_svg_v2(model, profile, decor_labels=None)` returns
  `(svg, info)`. It exports every seat that has a full identity (row label, number, unique) whatever the
  document status is. A seat without identity never reaches the SVG. `info` has `seat_count`,
  `omitted_seat_count`, `unavailable_seat_count`, `marked` (`PASS`, `QUARANTINE`, plus `SCHEMATIC`
  for the schematic lane), `viewBox`, `seats[]`. When no seat can be exported the SVG is null and
  `info.reason` is `no_identified_seats`.
- A QUARANTINE or schematic SVG is marked: the root has `data-validation-status` and
  `data-lane`, and `<desc>` says so. The importer ignores both.
- Unavailable seats are NOT seats in the SVG (fail closed): each is drawn as decor (a grey square
  without a label) and listed in `info.unavailable_seats`.
- Decor: a stage bar on `stage.side` when it is known, row labels on both ends of every row, section
  titles. Decor never uses an `inkscape:label` that starts with `#`.
- The authoring SVG is in pixel-like user units: `viewBox="0 0 W H"` equals the declared `width` and
  `height` (1 user unit = 1 px). The step of the hall is the median of every seat's nearest-neighbour
  distance and becomes `AUTHORING_GRID_STEP` = 24 units, so
  `svg x = model x * info.scale + info.offset.x` (same for y; a hall wider than 2000 is scaled down
  as a whole). The seat radius is 0.32 of a step (7.68), at most 0.4. The outline is the profile
  `stroke_width_px` in user units (default 1), at most a quarter of the radius; fonts of decor are
  fractions of a step. There is at least one step of room around the seats.
- For a v2 model converted from a PASS XLSX v1 model the seat coordinates (after the same scale and
  offset), radius and category are equal to what `build_authoring_svg` (v1) writes. The canvas of v2 can
  be wider (room for the row labels).
- Visual guard (`arena_export.outline_guard`, run inside `build_authoring_svg_v2`): a failing export is
  marked QUARANTINE in the SVG, `info.export_issue` holds the first reason code and `info.export_issues`
  all of them, and the service answers `validation_status` QUARANTINE. Codes:
  `export_outline_too_heavy` (the outline covers over 20 % or the seats over 60 % of the box of the
  seats), `export_seats_too_small` (the seat radius is under 5 user units, `MIN_SEAT_RADIUS`),
  `export_seats_overlap` (over 1 % of the seats are closer to their nearest neighbour than
  2 * r + outline).
- `hall_preview.render_preview_png(model, decor_labels=None, max_px=1600)` returns PNG bytes
  (Pillow only): available seats with numbers, seats without identity in orange with `?`, unavailable
  in grey, row labels on both sides, section titles, stage, a banner `QUARANTINE` or `SCHEMATIC`.

## 5. Structural lane: `hall-structure-v1` (spec 4.9)

Schema file: `hall_model/hall-structure-v1.schema.json`. No coordinates, no single seats.

```jsonc
{
  "schema_version": "hall-structure-v1",
  "stage": {"side": "top", "confidence": 0.9},
  "sections": [
    {"label": "Parterre", "confidence": 0.9,
     "placement": {"band": 0, "order": 0},     // optional: band 0 is nearest to the stage, order goes left to right
     "rows": [
       {"label": "1", "seat_count": 22, "confidence": 0.8,
        "numbering": {"rule": "left_to_right", "start": 1},   // null when the reader did not see it
        "aisles_after": [11]}                                   // seat positions from the left, 1-based
     ]}
  ]
}
```

`venue_generator.schematic.build_schematic_model(structure, low_confidence=0.7)` returns a v2 model with
`lane: "schematic"`, `confirmed: false`, tidy positions (rows centred, sections in bands away from the
stage), numbers with status `rule`, and reported issues: a row with confidence under the threshold gives
`row_count_mismatch`, a row without numbering gives seats without numbers, a null label gives
`row_label_unknown`, an unknown stage gives `stage_unknown`.
`venue_generator.schematic.relayout(model)` computes the positions of a schematic model again from its
sections, rows, seat counts and gaps.

## 6. Operations (spec 4.10): `venue_generator/operations.py`

`apply_operations(model, operations) -> (new_model, diff)`. Pure: the input is not changed. An unknown
operation name, an unknown parameter, a missing parameter or a bad value raises
`OperationError(op_index, code, message)` and nothing is applied. Rows and sections are addressed by id.

| Operation | Parameters |
|---|---|
| `set_row_seat_count` | `row_id`, `count` (1..600), `edge`: `start` (left), `end` (right), `both` |
| `set_numbering` | exactly one of `section_id`, `row_id`; `rule`: `left_to_right`, `right_to_left`, `from_center`; `start` (default 1) |
| `add_row` | `section_id`, `label`, `seat_count`, `position`: `{"before_row_id": id}` or `{"after_row_id": id}` or `"start"` or `"end"`; optional `numbering` |
| `remove_row` | `row_id` |
| `rename_row` | `row_id`, `label` |
| `split_section` | `section_id`, `by`: `row` with `after_row_id`, or `seat_position` with `after_position` (1-based from the left); `new_labels`: two strings |
| `merge_sections` | `section_ids` (2 or more), `label` |
| `rename_section` | `section_id`, `label` |
| `add_section` | `label`, `rows`: list of `{label, seat_count}`; optional `numbering`; optional `position`: `{"relative_to": section_id, "side": "above"｜"below"｜"left"｜"right"}` |
| `add_gap` | exactly one of `section_id` (all rows), `row_id`; exactly one of `after_position`, `after_number`; `width` (default 1) |
| `set_seats_unavailable` | `row_id`; exactly one of `numbers`, `seat_keys`; `reason` (1..200 chars) |
| `remove_seats` | `row_id`; exactly one of `numbers`, `seat_keys` |
| `set_stage_side` | `side`: `top`, `bottom`, `left`, `right` |

Behaviour:

- A row with a known rule (`left_to_right`, `right_to_left`, `from_center`) is numbered again by that rule
  after its seat count changes. A row with `explicit` or `unknown` numbering keeps its numbers; added seats
  get `number` null (the validator then reports `numbering_unknown`). Nothing is guessed.
- Labels written by an operation get `label_status: "confirmed"`; numbers written by a rule get `rule`.
- Schematic lane: after the operations the whole model is laid out again (`schematic.relayout`).
- Exact lane: geometry of untouched seats does not change. New seats continue the row at its own pitch
  and direction; a new row copies the nearest row of the section moved by the section's row pitch, later
  rows of that section move by the same vector; a new section is put outside the bounding box of the
  hall; a gap moves the seats on its right by `width` pitches. The model gets `geometry_edited: true`.
- After the operations the model is validated again.

`diff`:

```jsonc
{
  "changes": [
    {"op_index": 0, "op": "set_row_seat_count", "code": "row_seat_count_changed",
     "section": {"id": "s1", "label": "Parterre"}, "row": {"id": "s1-r7", "label": "7"},
     "before": {"seat_count": 20}, "after": {"seat_count": 18},
     "seats_added": 0, "seats_removed": 2, "seats_changed": 0}
  ],
  "totals": {"seat_count_before": 400, "seat_count_after": 398, "affected_seat_count": 2,
             "affected_fraction": 0.005, "requires_confirmation": false}
}
```

One operation gives one change line (spec 4.10: one operation is one row of the difference log).
`requires_confirmation` is true when `affected_seat_count` is more than half of
`max(seat_count_before, seat_count_after)`. `code` values: `row_seat_count_changed`, `numbering_set`,
`row_added`, `row_removed`, `row_renamed`, `section_split`, `sections_merged`, `section_renamed`,
`section_added`, `gap_added`, `seats_set_unavailable`, `seats_removed`, `stage_side_set`.

## 7. HTTP contract (port 8090, internal network only)

Every route except `GET /healthz` needs `Authorization: Bearer <VENUE_GEN_TOKEN>`. Without a correct
token the answer is 401 `{"error": {"code": "auth.required", "message": "…"}}`. The process refuses to
start when `VENUE_GEN_TOKEN` is empty, unless `VENUE_GEN_ALLOW_NO_TOKEN=test-only` is set.
Every error is `{"error": {"code", "message", ...}}`.

`generator_version` in every answer:
`{"service": "g2b-0.2.0", "commit": "<VENUE_GEN_COMMIT or unknown>", "model_schema": "seating-json-v2",
"operations": "1", "adapters": {"xlsx_seating_adapter": "0.1.0", "pdf_seating_adapter": "0.1.0",
"raster_seating_adapter": "0.1.0"}}`.

### POST /generate (multipart) → 202

| Field | Meaning |
|---|---|
| `files` | 1..10 parts, each at most `VENUE_GEN_MAX_UPLOAD_MB` (10), all together at most 3 times that |
| `kinds` | JSON array, one item per file in the same order: `xlsx`, `pdf`, `image`, `other` |
| `profile` | optional XLSX profile id (`hunter-v1`, `aribau-v1`, `synthetic-v1`); without it the adapter finds the layout |
| `raster_profile` | optional JSON, the colour profile of `raster_seating_adapter`; an image without it has no adapter |
| `target_profile` | optional JSON, the same as before |
| `decor_labels` | optional JSON `{"stage": "Сцена"}` |

Answer: `{"job_id", "status": "queued", "status_url": "/jobs/<id>"}`. Errors: 400 `request.invalid`,
`kinds.invalid`, `profile.unknown`, `raster_profile.invalid`, `target_profile.invalid`; 413 `file.too_large`
(one file, or the whole body: the body is counted while it is read, also without `Content-Length`);
503 `queue.full`. A file whose content is not what `kinds` says is NOT an error: the adapter is picked by
content and the file report says `detected_kind`. A file declared `other` never goes to an adapter.

File report `outcome` and `reason`:

| `outcome` | `reason` |
|---|---|
| `used`, `candidate` | null |
| `no_adapter` | `unsupported_content`, `no_seat_candidates` (XLSX or PDF without a seat), `raster_profile_required` |
| `failed` | `adapter_error`, `conversion_error`, `unpacked_too_large` (zip bomb guard) |

A failed file never fails the job.

`GET /jobs/{id}` when `status` is `done`:

```jsonc
{
  "job_id": "…", "status": "done",
  "result": "ok",                        // or "no_adapter": no file gave a single seat. Not an error.
  "validation_status": "QUARANTINE",     // null with no_adapter
  "generator_version": {…},
  "files": [{"index": 0, "declared_kind": "pdf", "detected_kind": "pdf", "sha256": "…", "size_bytes": 1,
             "adapter": "pdf_seating_adapter", "outcome": "used",   // used | candidate | no_adapter | failed
             "reason": null, "seat_count": 260, "validation_status": "QUARANTINE"}],
  "model": {…} ,                         // seating-json-v2, null with no_adapter
  "authoring_svg": "<?xml …", "svg_info": {…},      // authoring_svg is null when no seat has an identity
  "preview_png_base64": "…",
  "svg_url": "/jobs/<id>/svg", "preview_png_url": "/jobs/<id>/preview.png"
}
```

The model comes from ONE file: the candidate with status PASS, then the most sellable seats, then the most
seats, then the lowest index. Files are not merged. `status: "failed"` carries `error {code, message}` as
before (`timeout`, `memory_limit`, `worker_crashed`, `internal_error`). Job limits as before: one job at a
time, queue 20, 120 s, 2048 MB per job, results kept for 1 hour, at most `VENUE_GEN_MAX_KEPT_JOBS` (50)
finished jobs. `svg_url` and `preview_png_url` are null when there is no such file. `generator_version` is
in the answer for every job status.
`GET /jobs/{id}/svg` (409 `job.no_svg` when null), `GET /jobs/{id}/preview.png` (409 `job.no_preview`).
Other job errors: 404 `job.not_found`, 409 `job.not_ready`, 409 `job.failed`.
Removed from the G2a service: the field `file`, the key `report`, `GET /jobs/{id}/diagnostic.svg`, the codes
`file.not_xlsx`, `file.too_large_unpacked`, `job.quarantined`. A quarantined model now has an SVG.

### Rules for every JSON route

- The body is counted while it is read: more than `VENUE_GEN_MAX_MODEL_MB` (8) gives 413 `body.too_large`.
- `NaN`, `Infinity`, a number that overflows, nesting deeper than 64 and a wrong field type give 400
  `request.invalid`.
- At most `VENUE_GEN_SYNC_CONCURRENCY` (2) requests run at once. A request that waits longer than
  `VENUE_GEN_SYNC_WAIT_S` (10) gets 503 `busy`: try again.
- 422 `model.not_renderable`: the model is valid but the exporter cannot draw it.
- 500 `internal_error` has no exception text.

### POST /render (JSON) → 200

Request `{"model": <seating-json-v2>, "target_profile"?: {…}, "decor_labels"?: {…}}`.
Answer `{"model", "validation_status", "authoring_svg", "svg_info", "preview_png_base64", "generator_version"}`.
The model in the answer is the request model validated again (schematic lane: laid out again).
Errors: 400 `model.invalid` with `details` (list of schema errors, at most 20), 413 `body.too_large`
(more than `VENUE_GEN_MAX_MODEL_MB`, default 8).

### POST /schematic (JSON) → 200

Request `{"structure": <hall-structure-v1>, "target_profile"?, "decor_labels"?}`. Answer as `/render`.
Errors: 400 `structure.invalid`.

### POST /apply-operations (JSON) → 200

Request `{"model", "operations": [{"op": "set_row_seat_count", "row_id": "…", "count": 18, "edge": "end"}],
"dry_run"?: false, "confirm_large_change"?: false, "target_profile"?, "decor_labels"?}`.
Answer as `/render` plus `"diff"` and `"applied": true`. With `dry_run: true` the answer has only
`diff`, `applied: false` and `generator_version` (this is what "I understood it like this" shows).
Errors: 400 `model.invalid`; 422 `operation.invalid` with `op_index` and `reason` (the `OperationError`
code); 409 `operation.confirmation_required` with `diff` when the change touches more than half of the
seats and `confirm_large_change` is not true. At most 200 operations in one request (more gives 422
`operation.invalid` with `op_index` 200).
When at least one operation is applied the answer model has `confirmed: false` and
`diff.totals.confirmation_reset` says if it was true before: a changed hall must be confirmed again.
