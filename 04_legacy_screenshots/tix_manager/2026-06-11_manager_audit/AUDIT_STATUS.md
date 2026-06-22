# TixGear Manager audit status

## Confirmed by screenshots in this folder
- Top-level window: `TixGear Manager version 1.6 build 7921 of 10 Mar 2026 [Test zone]`
- Visible top tab strip in the current session:
  - Frontends
  - Acquiring
  - Fiscal data
  - Subscriptions
  - Widget
  - Notifications
  - News
  - MFC
- Visible sub-tabs under Frontends:
  - Operators
  - Event organizers
  - Trusted agents
  - Connections to ETS
  - Promotions
  - Agents

## Screenshot files captured so far
- `00_frontends_operators.png`
- `01_acquiring.png` ... `08_frontends.png`
- `09_frontends_operators.png` ... `14_frontends_agents.png`
- `15_frontends_operator_dropdown_open.png`
- `16_acquiring_agent_dropdown_open.png`
- `17_frontends_operator_field_dropdown.png`
- `18_frontends_operators_clean.png`
- `19_*` probe images
- `20_frontends_operators_baseline.png`
- `21_*` ... `28_*` top-tab attempts
- contact sheets: `contact_top_tabs.png`, `contact_probe_y.png`, `contact_operator_probe.png`, `contact_grid_y.png`, `contact_plus_probe.png`

## Static JAR inventory (from `TixManager.jar`)

### All known `MainFrame.*.tab.title` panes
- `MainFrame.acquiringPanel.tab.title`
- `MainFrame.agentSubsPanel.tab.title`
- `MainFrame.agentsPanel.tab.title`
- `MainFrame.authorityMainPanel.tab.title`
- `MainFrame.fiscalDataPanel.tab.title`
- `MainFrame.frontendsPanel.tab.title`
- `MainFrame.gatewaysPanel.tab.title`
- `MainFrame.mecsPanel.tab.title`
- `MainFrame.newsMainPanel.tab.title`
- `MainFrame.notificationsPanel.tab.title`
- `MainFrame.operatorsPanel.tab.title`
- `MainFrame.organizersPanel.tab.title`
- `MainFrame.promosPanel.tab.title`
- `MainFrame.salesAgentsPanel.tab.title`
- `MainFrame.userMainPanel.tab.title`
- `MainFrame.widgetPanel.tab.title`

### Dialog classes present in the JAR
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

## Important note
Synthetic mouse/keyboard navigation did **not yet** switch the top tabs in this X11 session, so the GUI screenshot set is **not yet exhaustive**. The JAR inventory above is the reliable source for the full panel/dialog surface discovered so far.
