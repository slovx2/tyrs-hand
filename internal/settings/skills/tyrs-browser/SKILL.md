---
name: tyrs-browser
description: Use Playwright, the Tyrs Hand Worker browser, or the Desktop browser for web navigation, UI interaction, screenshots, downloads, uploads, and local testing. Use whenever a task requires browser UI work; prefer purpose-built APIs or connectors for non-UI semantic operations.
---

# Tyrs Browser

Choose between Playwright, the Worker browser, and the Desktop browser.

## Choose the browser

Honor an explicit browser choice. When user identity or login state is required, prefer the Worker browser, then the Desktop browser. Otherwise, use Playwright or the Worker browser based on the task.

Use the `chrome` MCP when selecting Worker or Desktop. Do not control the same page through Playwright and MCP simultaneously.

The remaining MCP-specific instructions apply only to Worker and Desktop. Call `browser_select` only to inspect availability or make an intentional selection. A stale or closed tab does not invalidate the browser selection: list tabs again and obtain a fresh tab.

## Work with tabs

Use `browser_tabs` as follows:

- `list` returns `controlledTabs` and unclaimed `userTabs` separately.
- Select or close a controlled Agent tab by its stable `tabId`; never reuse an old list position.
- Claim a user tab only with the current short-lived `claimToken`. If it expires or the page changes, list again.
- Never close a user-origin tab or mark it deliverable/handoff.
- Mark an Agent tab `deliverable` only when its page is part of the requested result.
- Mark an Agent tab `handoff` only when the user must continue interacting with it; expect it to become visible.
- Leave ordinary Agent tabs as `omit`. Infrastructure closes them automatically when the turn ends.

Do not rely on a manual `finalize` call for correctness. Use it only when intentionally ending browser work early.

If browser control is interrupted by user input, stop. List tabs again and explicitly claim the desired user tab before resuming.

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

Use a delay only when the user explicitly asks for elapsed time or no observable condition exists. Keep timeouts narrow. After a timeout, follow the returned `recoveryAction`; a recoverable timeout does not justify reselecting the browser.

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

If the selected browser or required file tools are unavailable, report that directly and use another allowed browser when the task permits.
