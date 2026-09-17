---
name: tyrs-browser
description: Use the Tyrs Hand Worker browser, or the Desktop browser, for web navigation, UI interaction, screenshots, downloads, uploads, and local testing. Prefer the Worker browser. Use whenever a task requires browser UI work; prefer purpose-built APIs or connectors for non-UI semantic operations.
---

# Tyrs Browser

Use the Worker browser by default. The Desktop browser is available. Playwright is allowed only when the user explicitly asks for it.

Host browser tools, file exchange, local services, and local Git do not require a Control Workspace binding. Worker, SSH, and browser authentication still apply. Control automations and Forum publishing require an active Workspace binding.

Binding, unbinding, and owner changes take effect on the next turn without restarting Codex or Chrome. Existing conversations reload MCP configuration through normal resume; do not rewrite their history or create replacement conversations to restore tools. Historical dynamic tool lists may remain unchanged when the Codex protocol cannot update them.

## Choose the browser

Default to the Worker browser. Honor an explicit request for the Desktop browser or Playwright.

Use the `chrome` MCP when selecting Worker or Desktop. Do not control the same page through Playwright and MCP simultaneously.

The remaining MCP-specific instructions apply to Worker and Desktop. Call `browser_select` only to inspect availability or make an intentional selection. Keep the selection on worker unless the user asks for Desktop or the Worker browser is unavailable. A stale, closed, expired, or unresponsive tab does not invalidate the browser selection: open a new Agent tab and continue there. After three failed attempts on the current browser, you may switch browsers.

## Work with tabs

Use `browser_tabs` as follows:

- `list` returns `controlledTabs` and unclaimed `userTabs` separately.
- Select or close a controlled Agent tab by its stable `tabId`; never reuse an old list position.
- Claim a user tab only with the current short-lived `claimToken`. If it expires or the page changes, list again; if the tab is still unusable, open a new Agent tab.
- Never close a user-origin tab or mark it deliverable/handoff.
- Mark an Agent tab `deliverable` only when its page is part of the requested result.
- Mark an Agent tab `handoff` only when the user must continue interacting with it; expect it to become visible.
- Leave ordinary Agent tabs as `omit`. Infrastructure closes them automatically when the turn ends.

Do not rely on a manual `finalize` call for correctness. Use it only when intentionally ending browser work early.

If browser control is interrupted by user input, stop. List tabs again and explicitly claim the desired user tab before resuming.

If a tool times out, a claim token or tab lease expires, or the current tab is unresponsive, stop retrying that tab. Open a new Agent tab with `browser_tabs` `new` and continue the task there. After three failed attempts on the current browser, you may switch browsers.

## Observe before acting

Use `browser_find` for a targeted search and `browser_snapshot` when broader page structure is required. Prefer snapshot refs and stable accessible attributes.

Before an action:

- Confirm that the target is unique.
- Use the exact current ref or a demonstrably unique selector.
- Never guess CSS selectors, coordinates, or element positions.
- After navigation or a major DOM update, refresh the find/snapshot result.
- Do not retry the same failed locator unchanged. Re-observe and choose a new verified target.

Use screenshots for visual evidence, not as the default source of action coordinates.

## Wait for conditions

Prefer `browser_wait_for` conditions over fixed delays:

- locator state for element readiness or disappearance;
- text state for visible/hidden content;
- URL or load state for navigation;
- response state for a specific network completion.

Use a delay only when the user explicitly asks for elapsed time or no observable condition exists. Keep timeouts narrow. After a timeout, do not keep waiting on the same tab: open a new Agent tab and continue. After three failed attempts on the current browser, you may switch browsers.

## Forms and sensitive data

Use `browser_fill_form` for multiple fields so every target is validated before any field changes. Re-observe the form if validation fails.

Do not inspect or return:

- cookies or authorization headers;
- localStorage, sessionStorage, browser profiles, or session stores;
- passwords, password input values, API keys, secrets, or tokens.

Do not use `browser_evaluate` to bypass these restrictions. Treat redacted output as unavailable rather than trying another extraction path.

## Files, downloads, and local services

- Stage workspace files with `browser_files.stage_file` before upload.
- Import completed downloads with `browser_files.import_download`.
- Worker browser tools use the host Browser MCP and the configured host files directory directly.
- Desktop browser tools use the dedicated Browser Agent SSH channel; a Desktop disconnect must not affect Worker browser tools or other Desktop clients.
- For a service bound to a non-browser-visible interface, call `browser_expose_service` and navigate to the returned loopback endpoint.
- Use task lifetime by default; use review lifetime only for a page the user must inspect after the turn.
- Browser tokens are read by the Worker from its restricted token file. Never inspect, echo, copy, or return them.

If the selected browser or required file tools are unavailable, report that directly. Prefer the Worker browser before trying Desktop.
