package web

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func staticFile(t *testing.T, name string) string {
	t.Helper()
	b, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Every element the script looks up must exist in the page, or that part of the UI silently dies.
func TestEveryIDUsedByTheScriptExistsInThePage(t *testing.T) {
	html, js := staticFile(t, "index.html"), staticFile(t, "app.js")
	have := map[string]bool{}
	for _, m := range regexp.MustCompile(`\bid="([A-Za-z][\w-]*)"`).FindAllStringSubmatch(html, -1) {
		have[m[1]] = true
	}
	if len(have) < 20 {
		t.Fatalf("found only %d ids in index.html; the pattern probably broke", len(have))
	}
	used := map[string]bool{}
	for _, re := range []string{`\$\(\s*["']([\w-]+)["']\s*\)`, `getElementById\(\s*["']([\w-]+)["']\s*\)`, `querySelector\(\s*["']#([\w-]+)`} {
		for _, m := range regexp.MustCompile(re).FindAllStringSubmatch(js, -1) {
			used[m[1]] = true
		}
	}
	if len(used) < 15 {
		t.Fatalf("found only %d ids used by app.js; the pattern probably broke", len(used))
	}
	var missing []string
	for id := range used {
		if !have[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("app.js uses ids that index.html does not define: %v", missing)
	}
}

// Every /api/... path the script calls must be a route the server really has.
func TestEveryAPIPathCalledByTheScriptIsRegistered(t *testing.T) {
	js := staticFile(t, "app.js")
	paths := map[string]bool{}
	for _, m := range regexp.MustCompile(`["'`+"`"+`](/api/[a-z][a-z/]*)`).FindAllStringSubmatch(js, -1) {
		paths[strings.TrimRight(m[1], "/")] = true
	}
	if len(paths) < 8 {
		t.Fatalf("found only %d api paths: %v", len(paths), paths)
	}
	ts, _, _, _ := rig(t, true)
	for p := range paths {
		// The mux answers an unknown route with the plain text "404 page not found";
		// our own handlers answer with JSON, so a JSON 404 still proves the route exists.
		unrouted := 0
		for _, method := range []string{"GET", "POST"} {
			r := do(t, ts, method, p, nil, nil)
			if r.StatusCode == http.StatusNotFound && !strings.Contains(r.Header.Get("Content-Type"), "json") && !strings.Contains(r.Header.Get("Content-Type"), "image") {
				unrouted++
			}
		}
		if unrouted == 2 {
			t.Errorf("app.js calls %s but no handler is registered for it", p)
		}
	}
}

// The page must work offline and never phone home.
func TestNoExternalResources(t *testing.T) {
	ext := regexp.MustCompile(`(?i)(https?:)?//[a-z0-9.-]+\.[a-z]{2,}`)
	for _, name := range []string{"index.html", "app.js", "style.css", "favicon.svg"} {
		for _, m := range ext.FindAllString(staticFile(t, name), -1) {
			if strings.Contains(m, "www.w3.org") { // SVG namespace, not a request
				continue
			}
			t.Errorf("%s references an external address: %s", name, m)
		}
	}
	html := staticFile(t, "index.html")
	for _, bad := range []string{"<script src=\"http", "<link rel=\"stylesheet\" href=\"http", "@import"} {
		if strings.Contains(html, bad) || strings.Contains(staticFile(t, "style.css"), bad) {
			t.Errorf("found %q", bad)
		}
	}
}

// Text from the server is shown as text, never as markup.
func TestScriptNeverInsertsServerTextAsHTML(t *testing.T) {
	js := staticFile(t, "app.js")
	for _, bad := range []string{"innerHTML =", "innerHTML=", "outerHTML", "insertAdjacentHTML", "document.write", "eval("} {
		if strings.Contains(js, bad) {
			// innerHTML = "" (clearing a list) is harmless
			if bad == "innerHTML =" || bad == "innerHTML=" {
				if !regexp.MustCompile(`innerHTML\s*=\s*(""|'')`).MatchString(js) || regexp.MustCompile(`innerHTML\s*=\s*[^"'\s;]`).MatchString(js) || regexp.MustCompile(`innerHTML\s*=\s*["'][^"']`).MatchString(js) {
					t.Errorf("app.js assigns non-empty HTML (%s); use textContent", bad)
				}
				continue
			}
			t.Errorf("app.js contains %s", bad)
		}
	}
}

func TestPageBasics(t *testing.T) {
	html := staticFile(t, "index.html")
	for _, want := range []string{`lang="en"`, `name="viewport"`, "<title>", `aria-live`, "Advanced"} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html lacks %q", want)
		}
	}
	for _, jargon := range []string{"hidraw", "GET_DEVICE_INFO", "RGB565", "payload"} {
		// technical words may appear only inside the collapsed Advanced section
		main := html
		if i := strings.Index(html, `id="adv"`); i > 0 {
			main = html[:i]
		}
		if strings.Contains(main, jargon) {
			t.Errorf("%q appears in the main flow; keep jargon under Advanced", jargon)
		}
	}
}

// Browsers fetch the icon without the API token, so it must load like the page does.
func TestFaviconIsLinkedAndServed(t *testing.T) {
	if !strings.Contains(staticFile(t, "index.html"), `<link rel="icon" type="image/svg+xml" href="favicon.svg">`) {
		t.Error("the page does not link the icon")
	}
	ts, _, _, _ := rig(t, false)
	resp := do(t, ts, http.MethodGet, "/favicon.svg", nil, map[string]string{"X-Token": ""})
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode != http.StatusOK || !strings.HasPrefix(ct, "image/svg+xml") {
		t.Errorf("status %d, content type %q", resp.StatusCode, ct)
	}
}
