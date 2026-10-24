# برنامهٔ اندروید چادرها (Kotlin)

Android client for the tent-management system (حرم مطهر شاهچراغ). Talks to the Go backend in `../backend`.

```
android/
  core/   pure-JVM Kotlin: API client, offline queue + sync, session, validators, Jalali dates, all screen logic
          ("presenters" exposing StateFlow). No Android dependency -> unit-testable anywhere.   46 tests
  app/    Android application: Jetpack Compose (Material 3, RTL, Vazirmatn), CameraX + ML Kit scanner,
          Keystore-encrypted session, WorkManager background sync.
```

## Build in Android Studio

1. Open the `android/` folder (Android Studio Ladybug 2024.2+ / JDK 17; the Gradle wrapper downloads Gradle 8.9).
2. Set the server address. Release builds read `-PapiBaseUrl`, debug builds `-PapiBaseUrlDebug`
   (both default to `https://shch.badr-group.ir/`; use `http://10.0.2.2:8080/` for a backend on your computer, seen from the emulator).
   Put them in `~/.gradle/gradle.properties`:
   ```
   apiBaseUrl=https://shch.badr-group.ir/
   apiBaseUrlDebug=https://shch.badr-group.ir/
   ```
   Cleartext HTTP is allowed **only** in debug builds and only for `10.0.2.2`/`localhost`.
3. Run `app` on a device. Release: Build ▸ Generate Signed Bundle/APK (keystores are git-ignored).

Command line: `./gradlew :app:assembleDebug`, `./gradlew :app:assembleRelease -PapiBaseUrl=https://...`.

## Tests

```
./gradlew -c settings-core.gradle.kts :core:test        # unit tests, no Android SDK needed
scripts/run-integration.sh                              # same tests + a real backend (needs go, psql, redis, a Postgres superuser)
```

`BackendIntegrationTest` drives the real server: login, token refresh, shift, hand-out, return, offline queue → sync,
idempotent replays, dashboard stream.

## Behaviour worth knowing

* **Roles** decide the home menu (server, gate lead, team, workshop, service, office, admin). The server re-checks everything.
* **Offline**: hand-outs, returns, status changes, moves and socks are saved on the device when the network is down and sent
  automatically (connectivity callback, retry with back-off, WorkManager every 15 min). Every operation carries an `op_id`,
  so a replay can never create a duplicate. Registering a new pilgrim needs the network (identity check).
* **Security**: tokens encrypted with an Android Keystore AES-GCM key; `FLAG_SECURE` (no screenshots / recent-apps preview);
  `allowBackup=false`, no data in cloud backup; HTTPS only (except the debug emulator host); queue entries belong to the user who made them.
* **Scanner**: QR, Data Matrix, Code 128; beep + vibration per scan; torch; manual code entry for damaged labels.

## Status — read this

* `core` is compiled and tested (46 unit tests + integration against the real backend).
* `app` was written in an environment without access to Google's Maven repository, so **it has not been compiled yet**.
  The first `assembleDebug` (in Android Studio or the `android` GitHub workflow) may report small API/typing errors in the UI code;
  all business logic lives in `core` and is already verified. Not done yet: national-ID card scanning (OCR), launcher icon artwork, app signing config, instrumented UI tests.
* Fonts: Vazirmatn (SIL OFL 1.1), see `app/FONT-LICENSE-Vazirmatn-OFL.txt`.
