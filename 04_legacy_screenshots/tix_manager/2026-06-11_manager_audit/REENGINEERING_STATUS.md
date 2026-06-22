# TixGear / ARENA apps reengineering status

Date: 2026-06-11
Host: `acer-server`
GUI session: X11 on `DISPLAY=:1` under XFCE/TigerVNC
Java: OpenJDK 21.0.11 already installed

## Apps discovered in the folder
- `TixManager.jar`
  - Main-Class: `client.manager.TManager`
- `TixReporter.jar`
  - Main-Class: `client.reporter.TReporter`
- `TixEditor.jar`
  - Main-Class: `client.editor.TEditor`
- `TixCassa.jar`
  - Main-Class: `client.cassa.TCassa`

## Current evidence collected

### TixManager
Screenshots already captured in this folder:
- `00_frontends_operators.png` … `28_tab_mfc.png`
- `29_operator_plus_click.png` (no visible UI change from the click)
- root snapshot of the live GUI: `current_root.png`

Visible top-level tabs in the UI:
- Frontends
- Acquiring
- Fiscal data
- Subscriptions
- Widget
- Notifications
- News
- MFC

Visible Frontends subtabs:
- Operators
- Event organizers
- Trusted agents
- Connections to ETS
- Promotions
- Agents

### Other app auth dialogs captured
- `30_tixreporter_auth.png`
  - Title: `Authorization`
  - User Role shown: `Ticket Agent`
- `31_tixeditor_auth.png`
  - Title: `Authorization`
  - User Role shown: `Event Organizer`
- `32_tixcassa_auth.png`
  - Title: `Авторизация`
  - User Role shown: `Агент`

## Dialog classes found in the JARs

### TixManager
- `AccessibilityDialog`
- `AcqCurrenciesDialog`
- `AddAcquiringDialog`
- `AddAuthUserDialog`
- `AddAuthorityDialog`
- `AddFrontendDialog`
- `AddGatewayDialog`
- `AddNewsDialog`
- `AddNotificationDialog`
- `AddPromoCodeDialog`
- `AddPromoPackDialog`
- `AddPromoScopeDialog`
- `AuthDialog`

### TixReporter
- `AddReportParamsDialog`
- `AuthDialog`
- `ReportFilterDialog`
- `TicketsRefundDialog`
- `ZoneIdDialog`

### TixEditor
- `AboutDialog`
- `AddActionDialog`
- `AddActionEventDialog`
- `AddActionEventLimitDialog`
- `AddActionEventPriceDialog`
- `AddCityDialog`
- `AddCountryDialog`
- `AddMediumDialog`
- `AddQuotaFileDialog`
- `AddQuotaManualDialog`
- `AddSeatingPlanDialog`
- `AddVenueDialog`
- `AuthDialog`
- `ChCategoryDialog`
- `CopyPricingDialog`
- `DelEventsDialog`
- `ExportPlanDialog`
- `FindDialog`
- `GenresDialog`
- `LanguageDialog`
- `NameEditorDialog`
- `OrderCategoryDialog`
- `PreferenceDialog`
- `ReturnQuotaDialog`
- `SyncDialog`
- `ThumbnailDialog`
- `UserStyleDialog`

### TixCassa
- `AboutDialog`
- `AddMediumDialog`
- `AuthDialog`
- `BlankChoosingDialog`
- `CashPaymentDialog`
- `CountedWaitingDialog`
- `EnterFanIdDialog`
- `FindDialog`
- `FrontendChoosingDialog`
- `GoToPageDialog`
- `KKTSettingsDialog`
- `KdpTypingDialog`
- `LanguageDialog`
- `NameEditorDialog`
- `PaymentDialog`
- `PreferenceDialog`
- `PreviewDialog`
- `PrinterChoosingDialog`
- `ReturnAmountChoosingDialog`
- `ReturnTicketDialog`
- `SelectVenueDialog`
- `TariffChoosingDialog`
- `TerminalSettingsDialog`
- `ThumbnailDialog`
- `UserStyleDialog`
- `VerifyDialog`

## Useful notes for next steps
- The GUI is already live on `DISPLAY=:1`.
- The auth dialogs are visible, so once credentials are provided, the next capture should be the post-login main screens and any role-specific menus.
- Existing screenshots are in a clean numeric sequence and can be turned into a contact sheet for review.
