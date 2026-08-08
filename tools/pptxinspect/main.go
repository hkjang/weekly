package main

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: pptxinspect FILE.pptx")
		os.Exit(2)
	}
	reader, err := zip.OpenReader(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer reader.Close()
	files := make([]*zip.File, 0)
	for _, file := range reader.File {
		if strings.HasPrefix(file.Name, "ppt/slides/slide") && strings.HasSuffix(file.Name, ".xml") && !strings.Contains(file.Name, "_rels") {
			files = append(files, file)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	for _, file := range files {
		fmt.Println("==", file.Name)
		stream, err := file.Open()
		if err != nil {
			panic(err)
		}
		decoder := xml.NewDecoder(stream)
		cellIndex := 0
		shapeIndex := 0
		container := ""
		texts := []string{}
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				panic(err)
			}
			switch value := token.(type) {
			case xml.StartElement:
				if value.Name.Local == "tc" {
					cellIndex++
					container = fmt.Sprintf("cell %d", cellIndex)
					texts = nil
				} else if value.Name.Local == "sp" && container == "" {
					shapeIndex++
					container = fmt.Sprintf("shape %d", shapeIndex)
					texts = nil
				} else if value.Name.Local == "t" {
					var text string
					if err := decoder.DecodeElement(&text, &value); err != nil {
						panic(err)
					}
					if strings.TrimSpace(text) != "" {
						texts = append(texts, text)
					}
				}
			case xml.EndElement:
				if (value.Name.Local == "tc" && strings.HasPrefix(container, "cell")) || (value.Name.Local == "sp" && strings.HasPrefix(container, "shape")) {
					if len(texts) > 0 {
						fmt.Printf("%s: %q\n", container, strings.Join(texts, " | "))
					}
					container = ""
					texts = nil
				}
			}
		}
		stream.Close()
	}
}
