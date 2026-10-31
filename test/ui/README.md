# Admin UI browser tests

Playwright tests of the Waymark admin UI. CI runs them on every push and pull request (the `ui` job in `.github/workflows/build.yml`) against a freshly built Waymark started with `-noauth`.

Run them locally against a throwaway instance (not one you use: the tests create and delete a proxy rule, an ACL, a redirect and a trusted proxy, and change the status page layout):

```sh
cd src && go build -o /tmp/waymark . && cd -
mkdir -p /tmp/wm-ui && (cd /tmp/wm-ui && /tmp/waymark -noauth -port :8000 -mdns=false -default_inbound_port 8443 &)
cd test/ui
npm ci
npx playwright install chromium
BASE_URL=http://127.0.0.1:8000 npm test
```

A failed test saves a screenshot (`failure-*.png`) here. Any page error or native browser dialog (`alert`, `confirm`, `prompt`) fails the run.
