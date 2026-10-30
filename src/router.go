package main

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	csrf "imuslab.com/zoraxy/mod/csrfcompat" // stdlib CrossOriginProtection
)

// Static resources router for the admin console

func FSHandler(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow access to /script/*, /img/pubic/* and /login.html without authentication
		if strings.HasPrefix(r.URL.Path, "/script/") ||
			strings.HasPrefix(r.URL.Path, "/img/public/") ||
			r.URL.Path == "/login.html" ||
			r.URL.Path == "/reset.html" ||
			r.URL.Path == "/favicon.png" {
			if isHTMLFilePath(r.URL.Path) {
				handleInjectHTML(w, r, r.URL.Path)
				return
			}
			handler.ServeHTTP(w, r)
			return
		}

		// Check authentication
		if !authAgent.CheckAuth(r) && requireAuth {
			http.Redirect(w, r, "/login.html", http.StatusTemporaryRedirect)
			return
		}

		//For Plugin Routing
		if strings.HasPrefix(r.URL.Path, "/plugin.ui/") {
			//Extract the plugin ID from the request path
			parts := strings.Split(r.URL.Path, "/")
			if len(parts) > 2 {
				//There is always a prefix slash, so [2] is the plugin ID
				pluginID := parts[2]
				pluginManager.HandlePluginUI(pluginID, w, r)
			} else {
				http.Error(w, "Invalid Usage", http.StatusInternalServerError)
			}
			return
		}

		//Authenticated
		if isHTMLFilePath(r.URL.Path) {
			handleInjectHTML(w, r, r.URL.Path)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

func isHTMLFilePath(requestURI string) bool {
	return strings.HasSuffix(requestURI, ".html") || strings.HasSuffix(requestURI, "/")
}

// Serve the html file with template token injected
func handleInjectHTML(w http.ResponseWriter, r *http.Request, relativeFilepath string) {
	// Read the HTML file
	var content []byte
	var err error
	if len(relativeFilepath) > 0 && relativeFilepath[len(relativeFilepath)-1:] == "/" {
		relativeFilepath = relativeFilepath + "index.html"
	}
	if *development_build {
		// Load from disk; path.Clean keeps the file in web/
		targetFilePath := strings.ReplaceAll(filepath.Join("web/", path.Clean("/"+relativeFilepath)), "\\", "/")
		content, err = os.ReadFile(targetFilePath)
		if err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	} else {
		//Load from embedded fs, require trimming off the prefix slash for relative path
		relativeFilepath = strings.TrimPrefix(relativeFilepath, "/")
		relativeFilepath = filepath.ToSlash(filepath.Join("web/", relativeFilepath))
		content, err = webres.ReadFile(relativeFilepath)
		if err != nil {
			SystemWideLogger.Println("Load embedded web file failed: ", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	}

	// Convert the file content to a string
	htmlContent := string(content)

	//Defeine the system template for this request
	templateStrings := map[string]string{
		".csrfToken": csrf.Token(r),
	}

	// Replace template tokens in the HTML content
	for key, value := range templateStrings {
		placeholder := "{{" + key + "}}"
		htmlContent = strings.ReplaceAll(htmlContent, placeholder, value)
	}

	// Write the modified HTML content to the response
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(htmlContent))
}
