package main

import (
	"bytes"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/tools/go/vcs"

	"github.com/hirokidaichi/goviz/dotwriter"
	"github.com/hirokidaichi/goviz/goimport"
	"github.com/juju/ratelimit"
	"github.com/pkg/errors"
	"github.com/vrischmann/hutil"
)

func dot(r io.Reader, w io.Writer) error {
	var buf bytes.Buffer

	cmd := exec.Command("dot", "-Tpng")
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

func downloadPackage(importPath string) error {
	vcs.ShowCmd = true

	root, err := vcs.RepoRootForImportPath(importPath, true)
	if err != nil {
		return errors.Wrap(err, "repo root for import path")
	}
	if root != nil && (root.Root == "" || root.Repo == "") {
		return errors.New("empty repo root")
	}

	srcPath := filepath.Join(gopath, "src")
	localDirPath := filepath.Join(srcPath, root.Root, "..")
	fullLocalPath := filepath.Join(srcPath, root.Root)

	err = os.MkdirAll(localDirPath, 0700)
	if err != nil {
		return errors.Wrap(err, "mkdir all")
	}

	_, err = os.Stat(fullLocalPath)
	switch {
	case !os.IsNotExist(err) && err != nil:
		return errors.Wrap(err, "stat")

	case os.IsNotExist(err):
		log.Printf("create %s", root.Repo)

		err = root.VCS.Create(fullLocalPath, root.Repo)
		if err != nil {
			return errors.Wrap(err, "vcs create")
		}

	case err == nil:
		log.Printf("update %s", root.Repo)

		err = root.VCS.Download(fullLocalPath)
		if err != nil {
			return errors.Wrap(err, "vcs download")
		}
	}

	err = root.VCS.TagSync(fullLocalPath, "")
	if err != nil {
		return errors.Wrap(err, "vcs tag sync")
	}
	return nil
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
	plotLeaf := req.URL.Query().Get("leaf") == "true"

	if err := downloadPackage(packagePath); err != nil {
		log.Printf("%v", err)
		hutil.WriteText(w, http.StatusInternalServerError, "unable to download package %s", packagePath)
		return
	}

	factory := goimport.ParseRelation(packagePath, search, plotLeaf)
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

	err := dot(&buf, &buf2)
	if err != nil {
		log.Printf("%s", err)
		hutil.WriteText(w, http.StatusInternalServerError, "unable to call dot")
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(http.StatusOK)
	io.Copy(w, &buf2)
}

func envVar(key, def string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return def
}

var (
	flListenAddr = flag.String("l", envVar("LISTEN_ADDR", "localhost:3245"), "The listen address")
	gopath       = os.Getenv("GOPATH")
)

func main() {
	flag.Parse()

	if gopath == "" {
		log.Fatal("please set the GOPATH environment variable")
	}

	var chain hutil.Chain
	chain.Use(hutil.NewLoggingMiddleware(nil))

	mux := http.NewServeMux()
	mux.HandleFunc("/", renderHandler)

	log.Printf("listening on %s", *flListenAddr)
	http.ListenAndServe(*flListenAddr, chain.Handler(mux))
}
