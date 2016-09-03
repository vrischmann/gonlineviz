package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"os/exec"

	"github.com/hirokidaichi/goviz/dotwriter"
	"github.com/hirokidaichi/goviz/goimport"
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

func renderHandler(w http.ResponseWriter, req *http.Request) {
	packagePath := req.URL.Path

	if packagePath == "" {
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

func main() {
	http.HandleFunc("/", renderHandler)

	log.Println("listening on :3245")
	http.ListenAndServe(":3245", nil)
}
