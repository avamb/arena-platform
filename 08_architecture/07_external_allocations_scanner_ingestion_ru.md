# External Allocations, Scanner Federation, And Data Ingestion

Updated: 2026-06-21

## Decision

The scanner must remain a separate system boundary. It should validate tickets and barcodes from the new platform, legacy imports, complimentary tickets, and external ticketing platforms. Scanning must not assume every valid barcode was sold by the new platform.

The scanner system already exists, including an application and the `macs.arenasoldout.com` event/scanning surface. It currently synchronizes with Bil24 through webhooks. The new platform should preserve that integration style: scanner sync is driven by signed platform webhook notifications/events, not by embedding scanner logic into the platform core.

The platform must also support external quota allocation to third-party ticketing platforms. This is an older market practice, but it is still operationally necessary. Organizers should be able to see allocations, external sales, returns, scans, settlements, and exceptions in one platform window even when part of the data comes from external systems.

External data may arrive through structured APIs, webhooks, CSV/XLSX, email, PDF, screenshots, manual entry, or semi-structured reports. The architecture should include a normalization layer that can use deterministic parsers first and low-cost asynchronous AI normalization where needed. MCP servers or connector services can be used to expose external systems and files to ingestion workflows, but normalized platform records remain the source for reports.

## Scanner Federation Boundary

### Existing Scanner System

Current state:

- scanner system and application already exist
- scanner event surface is available under `macs.arenasoldout.com`
- current synchronization with Bil24 is webhook-driven
- the scanner should remain independently deployable and operationally separate

Target state:

- replace or supplement Bil24 webhook sync with new platform webhook notifications
- keep scanner contract event-driven
- avoid requiring scanner clients to poll platform APIs continuously
- preserve scanner's ability to validate non-platform barcodes
- publish scan results back to the platform event/reporting pipeline

Before implementation, the current scanner webhook contract must be captured:

- inbound event types from Bil24/current system
- payload fields
- auth/signature method
- retry/idempotency behavior
- barcode/ticket identifiers
- event/session mapping
- scan result callbacks, if any
- mobile/app offline behavior

### Principle

The scanner service validates a barcode against a ticket authority, not only against the platform's own ticket table.

Possible ticket authorities:

- platform-issued paid tickets
- platform-issued complimentary invitation tickets
- platform-issued replacement tickets
- imported legacy Bil24/TixGear tickets
- third-party platform quota tickets
- manually imported guest lists
- partner-provided barcode lists

### Required Concepts

- `TicketAuthority`
- `BarcodeNamespace`
- `BarcodeCredential`
- `ScanValidationPolicy`
- `ExternalTicketImport`
- `ExternalBarcodeBatch`
- `ScanEvent`
- `ScanDecision`
- `ScanReconciliation`

### Barcode Namespace Rules

Barcodes must be resolved with namespace awareness.

Required rules:

- one raw barcode string may not be globally assumed unique
- validation should consider event/session, authority, namespace, source system, and imported batch where possible
- scanner responses must identify the authority used for the decision
- collisions must produce a safe ambiguous/needs-review decision, not accidental entry
- external barcode batches must be immutable after activation except through explicit replacement/revocation batches

### Validation Modes

Supported validation modes:

- platform online validation
- scanner local/offline cache for platform tickets
- preloaded external barcode allowlist
- external platform online lookup where a contract exists
- manual guest list lookup
- fallback unresolved/needs-supervisor-review

The first implementation can use platform online validation plus imported barcode batches. The architecture must preserve the path to offline event-day operation and external online lookup.

### Scan Event Publishing

Scanner service publishes scan events back to the platform event backbone.

The platform stores:

- event/session
- scan timestamp
- raw barcode hash/reference
- ticket authority
- validation decision
- scanner device/user
- gate/location
- source batch/import where applicable
- duplicate scan markers
- manual override markers

Raw barcode values should be protected. Reports can use references/hashes unless the operator role explicitly needs raw values for support.

## External Quota Allocation

### Principle

An organizer may allocate part of event/session capacity to an external platform. The external platform may sell independently and later return sales, returns, scans, settlement, or unsold quota data.

The platform should make this visible and reconcilable without pretending that external orders are native platform orders.

### Required Concepts

- `ExternalPlatform`
- `ExternalAllocation`
- `ExternalAllocationItem`
- `ExternalQuota`
- `ExternalSalesReport`
- `ExternalReturnReport`
- `ExternalSettlementReport`
- `ExternalTicketImport`
- `ExternalReportIngestion`
- `ReconciliationRun`
- `ReconciliationException`

### Allocation Lifecycle

Recommended lifecycle:

```text
draft allocation
  -> approved allocation
  -> sent/exported to external platform
  -> acknowledged by external platform where possible
  -> sales/import reports received
  -> unsold quota returned or expired
  -> settlement and post-event reconciliation
```

### Inventory Rules

External quota must reserve or block capacity in the platform while allocated.

Rules:

- allocated quota reduces platform-sellable inventory
- external platform sales consume from allocated quota, not from normal platform checkout inventory
- returned unsold quota can be released back to platform inventory if event policy allows it
- over-sales and mismatched seat/category data become reconciliation exceptions
- assigned-seat allocations should preserve exact seat IDs when possible
- general-admission allocations can use quantity-based capacity blocks

### Organizer One-Window View

Organizer backoffice should show a unified event view:

- platform sales
- POS/cash sales
- complimentary invitations
- agent sales
- external platform allocations
- external platform reported sales
- external platform returns/refunds where reported
- imported external barcodes
- scan results by ticket authority
- reconciliation exceptions
- estimated vs confirmed settlement

The UI should distinguish trusted platform data from imported/external reported data.

## External Data Ingestion

### Source Types

Supported source types:

- API/webhook connector
- CSV/XLSX upload
- PDF report
- email attachment
- screenshot/image
- manual entry
- SFTP/object storage drop
- MCP server or connector service

### Normalization Pipeline

Recommended pipeline:

```text
source artifact
  -> artifact storage and checksum
  -> source classification
  -> deterministic parser if available
  -> low-cost/cold AI normalization if parser is unavailable or confidence is low
  -> schema validation
  -> confidence scoring
  -> human review for low-confidence or high-impact changes
  -> staging records
  -> reconciliation
  -> approved import into reporting/scan/allocation views
```

AI normalization is an assistive ingestion layer. It must not silently mutate inventory, orders, settlements, or scan decisions without validation and audit.

### MCP Server / Connector Role

An MCP server or connector service may be used to:

- expose external platform files, reports, or APIs to ingestion workflows
- provide authenticated access to external data sources
- run repeatable extraction jobs
- let operators or agents review extracted data through controlled tools

MCP/connector output must still pass platform schema validation, permission checks, confidence thresholds, and audit logging.

### Required Records

- source artifact reference
- original filename/source URL/email metadata where applicable
- checksum
- parser/model/connector version
- extracted structured rows
- confidence score
- mapping decisions
- reviewer and approval state
- import result
- reconciliation exceptions

## Reporting And Settlement

Reports must separate:

- native platform paid tickets
- POS/cash tickets
- complimentary tickets
- external allocated quota
- external reported sold tickets
- external imported barcodes
- external returned quota
- external refunds/voids where known
- scanned external tickets
- unresolved external discrepancies

Financial reports must clearly label external data as reported/estimated/confirmed depending on source quality and reconciliation status.

## Events

Recommended event families:

```text
external_platform.*
external_allocation.*
external_report.*
external_ticket_import.*
external_reconciliation.*
scanner_authority.*
```

Important examples:

```text
external_allocation.created
external_allocation.sent
external_allocation.returned
external_report.received
external_report.normalized
external_ticket_import.approved
external_reconciliation.exception_created
scanner_authority.updated
```

## Open Questions

1. Which external platforms currently receive ticket quotas?
2. Do external quotas need assigned-seat seat-level allocations first, general-admission quantity allocations first, or both?
3. What external data formats are common today: CSV, XLSX, PDF, email, screenshots, API, or portal export?
4. Should external barcode imports be accepted for scanning before sales reconciliation is complete?
5. What level of AI normalization is acceptable for financial reports: draft-only, review-required, or auto-approve above confidence threshold?
6. Which MCP/connector targets are likely first: email inbox, Google Drive folder, external platform portal, SFTP, or API?
7. What should happen when external reported sales exceed allocated quota?
