# Admin UI browser tests

Playwright tests of the Waymark admin UI. CI runs them on every push and pull request (the `ui` job in `.github/workflows/build.yml`) against a freshly built Waymark, signing in as an admin account the first test creates with the setup code Waymark prints at startup.

Run them locally against a throwaway instance with no admin account yet (not one you use: the first test creates the admin account `uitest`, and the tests create and delete rules, an ACL, a redirect and a trusted proxy, and change the status page layout). `WAYMARK_LOG` points the tests at Waymark's output, where they find the setup code (or pass it as `SETUP_CODE`). For an instance that already has an account, set `ADMIN_USER` and `ADMIN_PASSWORD` instead:

```sh
cd src && go build -o /tmp/waymark . && cd -
mkdir -p /tmp/wm-ui && (cd /tmp/wm-ui && /tmp/waymark -port :8000 -default_inbound_port 8443 > waymark.log 2>&1 &)
cd test/ui
npm ci
npx playwright install chromium
BASE_URL=http://127.0.0.1:8000 WAYMARK_LOG=/tmp/wm-ui/waymark.log npm test
```

A failed test saves a screenshot (`failure-*.png`) here. Any page error, native browser dialog (`alert`, `confirm`, `prompt`) or Content-Security-Policy violation fails the run.
