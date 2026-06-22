# Arena New Materials Index

Updated: 2026-06-21

This folder is now organized as a reference library for the new production backend architecture. Legacy Bil24/TixGear materials are kept as domain references and UI/process examples, not as implementation dependencies.

## Control Indexes

- `ARTIFACTS_MANIFEST.csv` - full file inventory with category, relative path, extension, size, timestamp, and SHA256 hash.
- `CATEGORY_SUMMARY.csv` - file counts and byte totals by top-level category.
- `EXTENSION_SUMMARY.csv` - file counts and byte totals by extension.
- `DEDUPLICATION_REPORT.csv` - exact SHA256 duplicate groups; repeated files moved to `99_archive_duplicates/exact_hash_duplicates/`.

## Folder Map

### `01_official_bil24_docs/`

Official Bil24 public documentation, API notes, and collected public links. Use this as a domain/process reference only.

Important files:
- `api/bil24_ticket_agent_api_notes_ru.md`
- `api/bil24_app_api_2023.docx`
- `public_site/bil24_public_links.txt`

### `02_product_and_market_research/`

Market, product strategy, and risk analysis for the ticketing platform direction.

Important files:
- `ticketing_market_research_ru.md`
- `production_strategy_risks_ru.md`

### `03_legacy_bil24_apps/`

Java desktop applications from the legacy Bil24/TixGear ecosystem. They are evidence for role separation, workflows, and feature surface.

Important folder:
- `java_applications/`

### `04_legacy_screenshots/`

Captured screenshots and audit notes from legacy desktop apps. Use these to reconstruct workflows, not visual style.

Important folders:
- `tix_manager/2026-06-11_manager_audit/`
- `tix_editor/2026-06-12_editor_audit/`
- `tix_cassa/2026-06-21_cassa_audit/`
- `raw_misc/`

### `05_widgets_and_site_templates/`

Legacy web widgets, Flutter widget builds, and generated site archives. Useful as examples of old checkout/widget integration.

Important folders:
- `widgets/legacy_widget_archives/`
- `sites/legacy_site_archives/`

### `06_venue_maps_and_seating/`

Venue maps, SVG seating assets, PDFs, and venue-maker inputs. Use as sample data for future venue/seating modules.

Important folders:
- `svg_library/`
- `venue_maker_assets/`

### `07_source_snippets_legacy/`

Small legacy source snippets and generated-site fragments. Useful as implementation examples only.

Important folder:
- `soldout_php_snippets/`

### `08_architecture/`

Working architecture notes for the new backend and platform design.

Important files:
- `00_backend_architecture_brief_ru.md`
- `01_api_compatibility_gateway_ru.md`
- `02_wordpress_integration_contract_ru.md`
- `03_platform_management_api_and_permissions_ru.md`
- `04_large_venue_performance_strategy_ru.md`
- `05_interface_taxonomy_and_complimentary_tickets_ru.md`
- `06_event_notifications_billing_reporting_ru.md`
- `07_external_allocations_scanner_ingestion_ru.md`
- `08_platform_superadmin_observability_ru.md`
- `09_domain_state_machines_ru.md`
- `10_compliance_security_privacy_ru.md`
- `11_architecture_decision_log_ru.md`
- `12_master_platform_specification_ru.md`
- `13_backend_go_initial_specification_ru.md`

### `09_autoforge/`

AutoForge handoff pack, guardrails, clarification register, critical audit and specification starter.

Important files:
- `README.md`
- `00_AGENT_GUARDRAILS.md`
- `01_CLARIFICATION_REGISTER.md`
- `02_CRITICAL_ARCHITECTURE_AUDIT.md`
- `03_SPECIFICATION_STARTER.md`

### `99_archive_duplicates/`

Quarantine for exact duplicate files. Nothing here was permanently deleted.

## Current Rule

New production code should not be placed into any legacy/reference folder. When implementation starts, create a separate application workspace, for example:

```text
apps/
services/
packages/
infra/
docs/
```
