package main

import (
	"bytes"
	"context"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/hirokidaichi/goviz/dotwriter"
	"github.com/hirokidaichi/goviz/goimport"
	"github.com/juju/ratelimit"
	"github.com/pkg/errors"
	"github.com/vrischmann/hutil"
)

func dot(ctx context.Context, r io.Reader, w io.Writer) error {
	var buf bytes.Buffer

	cmd := exec.CommandContext(ctx, "dot", "-Tpng")
	cmd.Stdin = r
	cmd.Stdout = w
	cmd.Stderr = &buf

	if err := cmd.Run(); err != nil {
		return errors.Wrapf(err, "output: %s", buf.String())
	}
	return nil
}

var (
	rl = ratelimit.NewBucketWithRate(10, 10)
)

func needsUpdate(packagePath string) bool {
	dir := filepath.Join(gopath, "src", packagePath)
	st, err := os.Stat(dir)

	switch {
	case err != nil:
		return true

	case time.Since(st.ModTime()) > 24*time.Hour:
		return true

	default:
		return false
	}
}

func goGet(ctx context.Context, packagePath string) error {
	cmd := exec.CommandContext(ctx, *flGoroot+"/bin/go", "get", "-u", packagePath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return errors.Wrap(err, "go install run")
	}
	return nil
}

func getCachedPNG(packagePath string, withLeaf bool) (io.Reader, error) {
	filename := "dot.png"
	if withLeaf {
		filename = "dot_leaf.png"
	}

	f, err := os.Open(filepath.Join(cacheDir, packagePath, filename))
	if err != nil {
		return nil, errors.Wrap(err, "os open")
	}
	return f, nil
}

func cachePNG(packagePath string, withLeaf bool, r io.Reader) (io.Reader, error) {
	dir := filepath.Join(cacheDir, packagePath)
	err := os.MkdirAll(dir, 0700)
	if err != nil {
		return nil, errors.Wrap(err, "os mkdirall")
	}

	filename := "dot.png"
	if withLeaf {
		filename = "dot_leaf.png"
	}

	f, err := os.Create(filepath.Join(dir, filename))
	if err != nil {
		return nil, errors.Wrap(err, "os create")
	}

	rd := io.TeeReader(r, f)

	return rd, nil
}

func renderHandler(w http.ResponseWriter, req *http.Request) {
	rl.Wait(1)

	packagePath := req.URL.Path
	if packagePath == "/" || packagePath == "" {
		hutil.WriteBadRequest(w, "Bad Request")
		return
	}

	if packagePath == "/favicon.ico" {
		hutil.WriteText(w, http.StatusNotFound, "Not Found")
		return
	}
	packagePath = packagePath[1:]

	search := req.URL.Query().Get("search")
	withLeaf := req.URL.Query().Get("leaf") == "true"

	if needsUpdate(packagePath) {
		if err := goGet(req.Context(), packagePath); err != nil {
			log.Printf("%v", err)
			hutil.WriteText(w, http.StatusInternalServerError, "unable to download package %s", packagePath)
			return
		}
	}

	if search == "" && !withLeaf {
		r, err := getCachedPNG(packagePath, withLeaf)
		if err == nil {
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusOK)
			io.Copy(w, r)
			return
		}
	}

	factory := goimport.ParseRelation(packagePath, search, withLeaf)
	if factory == nil {
		err := errors.Errorf("no package %s", packagePath)
		log.Printf("%v", err)
		hutil.WriteError(w, err)
		return
	}

	root := factory.GetRoot()
	if !root.HasFiles() {
		err := errors.Errorf("no .go files in %s", root.ImportPath)
		log.Printf("%v", err)
		hutil.WriteError(w, err)
		return
	}

	var buf bytes.Buffer

	writer := dotwriter.New(&buf)
	writer.MaxDepth = 128

	reversed := req.URL.Query().Get("reversed")

	switch {
	case reversed == "":
		writer.PlotGraph(root)

	default:
		writer.Reversed = true

		rroot := factory.Get(reversed)
		if rroot == nil {
			err := errors.Errorf("package %s does not exist", reversed)
			log.Printf("%v", err)
			hutil.WriteError(w, err)
			return
		}
		if !rroot.HasFiles() {
			err := errors.Errorf("package %s has no go files", reversed)
			log.Printf("%v", err)
			hutil.WriteError(w, err)
			return
		}

		writer.PlotGraph(rroot)
	}

	var buf2 bytes.Buffer

	err := dot(req.Context(), &buf, &buf2)
	if err != nil {
		log.Printf("%s", err)
		hutil.WriteText(w, http.StatusInternalServerError, "unable to call dot")
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(http.StatusOK)

	rd, err := cachePNG(packagePath, withLeaf, &buf2)
	if err != nil {
		log.Printf("%s", err)
		hutil.WriteText(w, http.StatusInternalServerError, "unable to cache image")
		return
	}

	io.Copy(w, rd)
}

func envVar(key, def string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return def
}

var (
	flListenAddr = flag.String("l", envVar("LISTEN_ADDR", "localhost:3245"), "The listen address")
	flGoroot     = flag.String("goroot", envVar("GOROOT", "/usr/local/go"), "The GOROOT variable")
	gopath       = os.Getenv("GOPATH")
	cacheDir     = filepath.Join(os.Getenv("HOME"), ".gonlineviz")
)

func main() {
	flag.Parse()

	if gopath == "" {
		log.Fatal("please set the GOPATH environment variable")
	}

	err := os.Mkdir(cacheDir, 0700)
	if !os.IsExist(err) && err != nil {
		log.Fatal(err)
	}

	var chain hutil.Chain
	chain.Use(hutil.NewLoggingMiddleware(nil))

	mux := http.NewServeMux()
	mux.HandleFunc("/", renderHandler)

	log.Printf("listening on %s", *flListenAddr)
	http.ListenAndServe(*flListenAddr, chain.Handler(mux))
}
