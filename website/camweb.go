/*
Copyright 2011 Google Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bytes"
	"flag"
	"fmt"
	"http"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path"
	"regexp"
	"strings"
	"template"
)

const defaultAddr = ":31798" // default webserver address

var h1TitlePattern = regexp.MustCompile(`<h1>(.+)</h1>`)

var (
	httpAddr            = flag.String("http", defaultAddr, "HTTP service address (e.g., '"+defaultAddr+"')")
	root                = flag.String("root", "", "Website root (parent of 'static', 'content', and 'tmpl")
	gitwebScript        = flag.String("gitwebscript", "/usr/lib/cgi-bin/gitweb.cgi", "Path to gitweb.cgi, or blank to disable.")
	gitwebFiles         = flag.String("gitwebfiles", "/usr/share/gitweb", "Path to gitweb's static files.")
	logDir              = flag.String("logdir", "-", "Directory to write log files to (one per hour), or empty to not log.")
	logStdout           = flag.Bool("logstdout", true, "Write to stdout?")
	pageHtml, errorHtml *template.Template
)

var fmap = template.FormatterMap{
	"":         textFmt,
	"html":     htmlFmt,
	"html-esc": htmlEscFmt,
}

// Template formatter for "" (default) format.
func textFmt(w io.Writer, format string, x ...interface{}) {
	writeAny(w, false, x[0])
}

// Template formatter for "html" format.
func htmlFmt(w io.Writer, format string, x ...interface{}) {
	writeAny(w, true, x[0])
}


// Template formatter for "html-esc" format.
func htmlEscFmt(w io.Writer, format string, x ...interface{}) {
	var buf bytes.Buffer
	writeAny(&buf, false, x[0])
	template.HTMLEscape(w, buf.Bytes())
}

// Write anything to w; optionally html-escaped.
func writeAny(w io.Writer, html bool, x interface{}) {
	switch v := x.(type) {
	case []byte:
		writeText(w, v, html)
	case string:
		writeText(w, []byte(v), html)
	default:
		if html {
			var buf bytes.Buffer
			fmt.Fprint(&buf, x)
			writeText(w, buf.Bytes(), true)
		} else {
			fmt.Fprint(w, x)
		}
	}
}

// Write text to w; optionally html-escaped.
func writeText(w io.Writer, text []byte, html bool) {
	if html {
		template.HTMLEscape(w, text)
		return
	}
	w.Write(text)
}


func applyTemplate(t *template.Template, name string, data interface{}) []byte {
	var buf bytes.Buffer
	if err := t.Execute(data, &buf); err != nil {
		log.Printf("%s.Execute: %s", name, err)
	}
	return buf.Bytes()
}

func servePage(w http.ResponseWriter, title, subtitle string, content []byte) {
	d := struct {
		Title    string
		Subtitle string
		Content  []byte
	}{
		title,
		subtitle,
		content,
	}

	if err := pageHtml.Execute(&d, w); err != nil {
		log.Printf("godocHTML.Execute: %s", err)
	}
}

func readTemplate(name string) *template.Template {
	fileName := path.Join(*root, "tmpl", name)
	data, err := ioutil.ReadFile(fileName)
	if err != nil {
		log.Exitf("ReadFile %s: %v", fileName, err)
	}
	t, err := template.Parse(string(data), fmap)
	if err != nil {
		log.Exitf("%s: %v", fileName, err)
	}
	return t
}

func readTemplates() {
	pageHtml = readTemplate("page.html")
	errorHtml = readTemplate("error.html")
}

func serveError(w http.ResponseWriter, r *http.Request, relpath string, err os.Error) {
	contents := applyTemplate(errorHtml, "errorHtml", err) // err may contain an absolute path!
	w.WriteHeader(http.StatusNotFound)
	servePage(w, "File "+relpath, "", contents)
}

func mainHandler(rw http.ResponseWriter, req *http.Request) {
	relPath := req.URL.Path[1:] // serveFile URL paths start with '/'
	if strings.Contains(relPath, "..") {
		return
	}

	if strings.HasPrefix(relPath, "gw/") {
		path := relPath[3:]
		http.Redirect(rw, req, "/code/?p=camlistore.git;f=" + path + ";hb=master", http.StatusFound)
		return
	}

	absPath := path.Join(*root, "content", relPath)
	fi, err := os.Lstat(absPath)
	if err != nil {
		log.Print(err)
		serveError(rw, req, relPath, err)
		return
	}
	if fi.IsDirectory() {
		relPath += "/index.html"
		absPath = path.Join(*root, "content", relPath)
		fi, err = os.Lstat(absPath)
		if err != nil {
			log.Print(err)
			serveError(rw, req, relPath, err)
			return
		}
	}

	switch {
	case fi.IsRegular():
		serveFile(rw, req, relPath, absPath)
	}
}

func serveFile(rw http.ResponseWriter, req *http.Request, relPath, absPath string) {
	data, err := ioutil.ReadFile(absPath)
	if err != nil {
		serveError(rw, req, absPath, err)
		return
	}

	title := ""
	if m := h1TitlePattern.FindSubmatch(data); len(m) > 1 {
		title = string(m[1])
	}

	servePage(rw, title, "", data)
}

type gitwebHandler struct {
	Cgi    http.Handler
	Static http.Handler
}

func (h *gitwebHandler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.URL.RawPath == "/code/" ||
		strings.HasPrefix(r.URL.RawPath, "/code/?") {
		h.Cgi.ServeHTTP(rw, r)
	} else {
		h.Static.ServeHTTP(rw, r)
	}
}

type noWwwHandler struct {
	Handler http.Handler
}

func (h *noWwwHandler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	host := strings.ToLower(r.Host)
	if host == "www.camlistore.org" {
		http.Redirect(rw, r, "http://camlistore.org" + r.URL.RawPath, http.StatusFound)
		return
	}
	h.Handler.ServeHTTP(rw, r)
}

func main() {
	flag.Parse()
	readTemplates()

	if *root == "" {
		var err os.Error
		*root, err = os.Getwd()
		if err != nil {
			log.Exitf("Failed to getwd: %v", err)
		}
	}

	mux := http.DefaultServeMux
	mux.Handle("/favicon.ico", http.FileServer(path.Join(*root, "static"), "/"))
	mux.Handle("/robots.txt", http.FileServer(path.Join(*root, "static"), "/"))
	mux.Handle("/static/", http.FileServer(path.Join(*root, "static"), "/static/"))

	testCgi := &CgiHandler{ExecutablePath: path.Join(*root, "test.cgi"),
		Root: "/test.cgi",
	}
	mux.Handle("/test.cgi", testCgi)
	mux.Handle("/test.cgi/foo", testCgi)

	mux.Handle("/code", http.RedirectHandler("/code/", http.StatusFound))
	if *gitwebScript != "" {
		env := os.Environ()
		env = append(env, "GITWEB_CONFIG="+path.Join(*root, "gitweb-camli.conf"))
		env = append(env, "CAMWEB_ROOT="+path.Join(*root))
		mux.Handle("/code/", &gitwebHandler{
			Cgi: &CgiHandler{
				ExecutablePath: *gitwebScript,
				Root:           "/code/",
				Environ:        env,
			},
			Static: http.FileServer(*gitwebFiles, "/code/"),
		})
	}
	mux.HandleFunc("/", mainHandler)

	var handler http.Handler = &noWwwHandler{Handler: mux}
	if *logDir != "" || *logStdout {
		handler = NewLoggingHandler(handler, *logDir, *logStdout)
	}
	if err := http.ListenAndServe(*httpAddr, handler); err != nil {
		log.Exitf("ListenAndServe %s: %v", *httpAddr, err)
	}
}
