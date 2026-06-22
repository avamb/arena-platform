# ARENA / TixGear screenshot reengineering notes

## Environment
- GUI session on `acer-server` (DISPLAY=:1).
- Test zone toggle in auth dialogs: `Alt+Z`.
- `TixReporter` required role `Operator` to log in with the tested credentials.

## Verified screenshot anchors

### TixManager
- `manager_current_for_tabs.png` — current main window state.
- `tixmanager_tabs_contact_old.png` — contact sheet showing the main tab audit set.
- `manager_tabs_contact_29_42.png` — later contact sheet of the same audit set.
- Key tab screenshots already saved in the folder with prefixes `00_`..`19_` and `29_`..`42_`.

### TixReporter
- `reporter_login_tz.png` — successful login in test zone.
- `reporter_after_login.png` — main window after login.

### TixEditor
- `editor_auth_current.png` / `editor_login_try.png` — auth dialog and failed login attempt.
- `editor_eventorg_login.png` — failed login with `Event Organizer` selected.

### TixCassa
- `cassa_auth_current.png` / `cassa_login_try.png` — auth dialog and failed login attempt.
- `cassa_login_agent_try.png` — failed login attempt on `Agent`.

## Notes
- `TixEditor` and `TixCassa` did not accept the tested credentials during this session.
- For any future pass, keep the correct auth window frontmost before using `Alt+Z`.
- The manager tab inventory from JAR inspection includes:
  - Frontends
  - Acquiring
  - Fiscal data
  - Subscriptions
  - Widget
  - Notifications
  - News
  - MEC
  - Operators
  - Event organizers
  - Trusted agents
  - Connections to ETS
  - Promotions
  - Agents
