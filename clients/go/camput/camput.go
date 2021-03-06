// Copyright 2010 Brad Fitzpatrick <brad@danga.com>
//
// See LICENSE.

package main

import (
	"camli/blobref"
	"camli/client"
	"camli/schema"
	"camli/jsonsign"
	"crypto/sha1"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
)

// Things that can be uploaded.  (at most one of these)
var flagBlob = flag.Bool("blob", false, "upload a file's bytes as a single blob")
var flagFile = flag.Bool("file", false, "upload a file's bytes as a blob, as well as its JSON file record")
var flagPermanode = flag.Bool("permanode", false, "create a new permanode")
var flagInit = flag.Bool("init", false, "first-time configuration.")
var flagShare = flag.Bool("share", false, "create a camli share by haveref with the given blobrefs")
var flagTransitive = flag.Bool("transitive", true, "share the transitive closure of the given blobrefs")

var flagVerbose = flag.Bool("verbose", false, "be verbose")

var wereErrors = false

type Uploader struct {
	*client.Client
}

func blobDetails(contents io.ReadSeeker) (bref *blobref.BlobRef, size int64, err os.Error) {
	s1 := sha1.New()
	contents.Seek(0, 0)
	size, err = io.Copy(s1, contents)
	if err == nil {
		bref = blobref.FromHash("sha1", s1)
	}
	return
}

func (up *Uploader) UploadFileBlob(filename string) (*client.PutResult, os.Error) {
	if *flagVerbose {
		log.Printf("Uploading filename: %s", filename)
	}
	file, err := os.Open(filename, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}

	ref, size, err := blobDetails(file)
	if err != nil {
		return nil, err
	}
	file.Seek(0, 0)
	handle := &client.UploadHandle{ref, size, file}
	return up.Upload(handle)
}

func (up *Uploader) UploadFile(filename string) (*client.PutResult, os.Error) {
	fi, err := os.Lstat(filename)
        if err != nil {
                return nil, err
        }

	m := schema.NewCommonFileMap(filename, fi)
	
	switch {
	case fi.IsRegular():
		// Put the blob of the file itself.  (TODO: smart boundary chunking)
		// For now we just store it as one range.
		blobpr, err := up.UploadFileBlob(filename)
		if err != nil {
			return nil, err
		}
		parts := []schema.ContentPart{ {BlobRef: blobpr.BlobRef, Size: blobpr.Size }}
		if blobpr.Size != fi.Size {
			// TODO: handle races of file changing while reading it
			// after the stat.
		}
		if err = schema.PopulateRegularFileMap(m, fi, parts); err != nil {
			return nil, err
		}
	case fi.IsSymlink():
		if err = schema.PopulateSymlinkMap(m, filename); err != nil {
			return nil, err
                }
	case fi.IsDirectory():
		ss := new(schema.StaticSet)
		dir, err := os.Open(filename, os.O_RDONLY, 0)
		if err != nil {
                        return nil, err
                }
		dirNames, err := dir.Readdirnames(-1)
		if err != nil {
                        return nil, err
                }
		dir.Close()
		sort.SortStrings(dirNames)
		// TODO: process dirName entries in parallel
		for _, dirEntName := range dirNames {
			pr, err := up.UploadFile(filename + "/" + dirEntName)
			if err != nil {
				return nil, err
			}
			ss.Add(pr.BlobRef)
		}
		sspr, err := up.UploadMap(ss.Map())
		if err != nil {
                                return nil, err
                }
                schema.PopulateDirectoryMap(m, sspr.BlobRef)
	case fi.IsBlock():
		fallthrough
	case fi.IsChar():
		fallthrough
	case fi.IsSocket():
		fallthrough
	case fi.IsFifo():
		fallthrough
	default:
		return nil, schema.UnimplementedError
	}

	mappr, err := up.UploadMap(m)
	return mappr, err
}

func (up *Uploader) UploadMap(m map[string]interface{}) (*client.PutResult, os.Error) {
	json, err := schema.MapToCamliJson(m)
	if err != nil {
                return nil, err
        }
	if *flagVerbose {
		fmt.Printf("json: %s\n", json)
	}
	return up.Upload(client.NewUploadHandleFromString(json))
}

func (up *Uploader) SignMap(m map[string]interface{}) (string, os.Error) {
	camliSigBlobref := up.Client.SignerPublicKeyBlobref()
	if camliSigBlobref == nil {
		// TODO: more helpful error message
		return "", os.NewError("No public key configured.")
	}

	m["camliSigner"] = camliSigBlobref.String()
	unsigned, err := schema.MapToCamliJson(m)
	if err != nil {
		return "", err
	}
	sr := &jsonsign.SignRequest{
		UnsignedJson: unsigned,
		Fetcher: up.Client.GetBlobFetcher(),
		UseAgent: true,
	} 
	return sr.Sign()
}

func (up *Uploader) UploadNewPermanode() (*client.PutResult, os.Error) {
	unsigned := schema.NewUnsignedPermanode()

	signed, err := up.SignMap(unsigned)
	if err != nil {
		return nil, err
	}

	return up.Upload(client.NewUploadHandleFromString(signed))
}

func (up *Uploader) UploadShare(target *blobref.BlobRef, transitive bool) (*client.PutResult, os.Error) {
	unsigned := schema.NewShareRef(schema.ShareHaveRef, target, transitive)

	signed, err := up.SignMap(unsigned)
	if err != nil {
		return nil, err
	}

	return up.Upload(client.NewUploadHandleFromString(signed))
}

func sumSet(flags ...*bool) (count int) {
	for _, f := range flags {
		if *f {
			count++
		}
	}
	return
}

func usage(msg string) {
	if msg != "" {
		fmt.Println("Error:", msg)
	}
	fmt.Println(`
Usage: camput

  camput --init       # first time configuration
  camput --blob <filename(s) to upload as blobs>
  camput --file <filename(s) to upload as blobs + JSON metadata>
  camput --share <blobref to share via haveref> [--transitive]
`)
	flag.PrintDefaults()
	os.Exit(1)
}

func handleResult(what string, pr *client.PutResult, err os.Error) {
	if err != nil {
		log.Printf("Error putting %s: %s", what, err)
		wereErrors = true
		return
	}
	if *flagVerbose {
		fmt.Printf("Put %s: %q\n", what, pr)
	} else {
		fmt.Println(pr.BlobRef.String())
	}
}

func main() {
	flag.Parse()

	if sumSet(flagFile, flagBlob, flagPermanode, flagInit, flagShare) != 1 {
		// TODO: say which ones are conflicting
		usage("Conflicting mode options.")
	}

	client := client.NewOrFail()
	if !*flagVerbose {
		client.SetLogger(nil)
	}
	uploader := &Uploader{client}

	switch {
	case *flagInit:
		doInit()
		return
	case *flagPermanode:
		if flag.NArg() > 0 {
			log.Exitf("--permanode doesn't take any additional arguments")
		}
		pr, err := uploader.UploadNewPermanode()
		handleResult("permanode", pr, err)
	case *flagFile || *flagBlob:
		for n := 0; n < flag.NArg(); n++ {
			if *flagBlob {
				pr, err := uploader.UploadFileBlob(flag.Arg(n))
				handleResult("blob", pr, err)
			} else {
				pr, err := uploader.UploadFile(flag.Arg(n))
				handleResult("file", pr, err)
			}
		}
	case *flagShare:
		if flag.NArg() != 1 {
			log.Exitf("--share only supports one blobref")
		}
		br := blobref.Parse(flag.Arg(0))
		if br == nil {
			log.Exitf("BlobRef is invalid: %q", flag.Arg(0))
		}
		pr, err := uploader.UploadShare(br, *flagTransitive)
		handleResult("share", pr, err)
	}

	if *flagVerbose {
		stats := uploader.Stats()
		log.Printf("Client stats: %s", stats.String())
	}
	if wereErrors {
		os.Exit(2)
	}
}
