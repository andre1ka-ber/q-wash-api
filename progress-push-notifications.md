# Progress: push notifications (see plan.md)

- [x] 1. Migration 000024 (device_tokens, notifications.kind/channel) — file written, models/repos next
- [x] 2. internal/platform/push (Sender, FCM v1, noop) + config
- [x] 3. device tokens: PUT/DELETE /me/devices + tests
- [x] 4. stage notifications on washing/ready (queue notifier hook) + tests
- [x] 5. scheduler: reminder_1h, late_5m + tests
- [x] 6. docs (API.md, openapi.yaml, DATA_MODEL.md, PROGRESS.md, .env.example)
- [x] 7. Flutter: deps, firebase setup, token service, foreground/tap handling, tests

Blocked externally: real FCM delivery needs the Firebase project/keys (not provided yet) —
everything is built against a fake sender; FCM stays disabled until env vars are set.

Remaining (manual, needs the Firebase project): google-services.json is in now (`q-wash/android/app/`, gradle plugin auto-applies since the file exists); GoogleService-Info.plist is in now too (`q-wash/ios/Runner/`, wired into the Xcode project) but push capability + APNs key still need doing in Xcode/Apple Developer (no macOS here to do it); FCM_PROJECT_ID / FCM_SERVICE_ACCOUNT_FILE on the server, then a real end-to-end check.
