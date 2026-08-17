package app

import (
	"fmt"
	"strings"
)

// Drawing primitives for generated slides. Sizes are EMU (914400 per inch).

type textRun struct {
	Text        string
	Size        int // hundredths of a point, 1400 == 14pt
	Bold        bool
	Color       string
	Indent      int // nesting level, 0 for a top level line
	Bullet      string
	SpaceBefore int
}

type shapeStyle struct {
	Fill         string // empty for no fill
	Line         string // empty for no outline
	Radius       bool   // rounded rectangle
	AnchorMiddle bool
}

// textBox renders a block of paragraphs inside an optional filled rectangle.
func textBox(id int, name string, x, y, cx, cy int, style shapeStyle, runs []textRun) string {
	geometry := `<a:prstGeom prst="rect"><a:avLst/></a:prstGeom>`
	if style.Radius {
		geometry = `<a:prstGeom prst="roundRect"><a:avLst><a:gd name="adj" fmla="val 8000"/></a:avLst></a:prstGeom>`
	}
	fill := `<a:noFill/>`
	if style.Fill != "" {
		fill = `<a:solidFill><a:srgbClr val="` + style.Fill + `"/></a:solidFill>`
	}
	line := `<a:ln><a:noFill/></a:ln>`
	if style.Line != "" {
		line = `<a:ln w="12700"><a:solidFill><a:srgbClr val="` + style.Line + `"/></a:solidFill></a:ln>`
	}
	anchor := "t"
	if style.AnchorMiddle {
		anchor = "ctr"
	}
	var body strings.Builder
	for _, run := range runs {
		body.WriteString(paragraphXML(run))
	}
	if body.Len() == 0 {
		body.WriteString(paragraphXML(textRun{Text: "", Size: 1200, Color: "475569"}))
	}
	return fmt.Sprintf(`<p:sp><p:nvSpPr><p:cNvPr id="%d" name="%s"/><p:cNvSpPr txBox="1"/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm>%s%s%s</p:spPr><p:txBody><a:bodyPr wrap="square" lIns="118872" rIns="118872" tIns="77724" bIns="77724" anchor="%s"><a:normAutofit/></a:bodyPr><a:lstStyle/>%s</p:txBody></p:sp>`,
		id, escapeXML(name), x, y, cx, cy, geometry, fill, line, anchor, body.String())
}

func paragraphXML(run textRun) string {
	bold := "0"
	if run.Bold {
		bold = "1"
	}
	color := run.Color
	if color == "" {
		color = "1E293B"
	}
	size := run.Size
	if size <= 0 {
		size = 1200
	}
	margin := run.Indent * 200000
	bullet := `<a:buNone/>`
	if run.Bullet != "" {
		bullet = `<a:buFont typeface="Arial"/><a:buChar char="` + escapeXML(run.Bullet) + `"/>`
	}
	spaceBefore := run.SpaceBefore
	if spaceBefore < 0 {
		spaceBefore = 0
	}
	properties := fmt.Sprintf(`<a:pPr marL="%d" indent="%d" algn="l"><a:lnSpc><a:spcPct val="105000"/></a:lnSpc><a:spcBef><a:spcPts val="%d"/></a:spcBef>%s</a:pPr>`,
		margin, -140000*boolToInt(run.Bullet != ""), spaceBefore, bullet)
	if strings.TrimSpace(run.Text) == "" {
		return `<a:p>` + properties + `<a:endParaRPr lang="ko-KR" sz="` + fmt.Sprint(size) + `"/></a:p>`
	}
	return `<a:p>` + properties +
		fmt.Sprintf(`<a:r><a:rPr lang="ko-KR" sz="%d" b="%s" dirty="0"><a:solidFill><a:srgbClr val="%s"/></a:solidFill><a:latin typeface="Aptos"/><a:ea typeface="Noto Sans KR"/><a:cs typeface="Arial"/></a:rPr><a:t>%s</a:t></a:r>`, size, bold, color, escapeXML(run.Text)) +
		fmt.Sprintf(`<a:endParaRPr lang="ko-KR" sz="%d" b="%s"/>`, size, bold) + `</a:p>`
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// pictureFrame places an image, letterboxed inside the given box so a wide
// screenshot and a tall one both keep their aspect ratio.
func pictureFrame(id int, name, relID string, boxX, boxY, boxCX, boxCY, imageWidth, imageHeight int) string {
	x, y, cx, cy := fitInside(boxX, boxY, boxCX, boxCY, imageWidth, imageHeight)
	return fmt.Sprintf(`<p:pic><p:nvPicPr><p:cNvPr id="%d" name="%s"/><p:cNvPicPr><a:picLocks noChangeAspect="1"/></p:cNvPicPr><p:nvPr/></p:nvPicPr><p:blipFill><a:blip r:embed="%s"/><a:stretch><a:fillRect/></a:stretch></p:blipFill><p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom><a:ln w="12700"><a:solidFill><a:srgbClr val="E2E8F0"/></a:solidFill></a:ln></p:spPr></p:pic>`,
		id, escapeXML(name), relID, x, y, cx, cy)
}

// fitInside scales the image to fit the box and centres it.
func fitInside(boxX, boxY, boxCX, boxCY, imageWidth, imageHeight int) (int, int, int, int) {
	if imageWidth <= 0 || imageHeight <= 0 {
		return boxX, boxY, boxCX, boxCY
	}
	scaledHeight := boxCX * imageHeight / imageWidth
	width, height := boxCX, scaledHeight
	if scaledHeight > boxCY {
		height = boxCY
		width = boxCY * imageWidth / imageHeight
	}
	if width <= 0 || height <= 0 {
		return boxX, boxY, boxCX, boxCY
	}
	return boxX + (boxCX-width)/2, boxY + (boxCY-height)/2, width, height
}

type tableColumn struct {
	Width int
	Title string
}

type tableCell struct {
	Text  string
	Bold  bool
	Color string
	Align string
}

// tableShape renders a header row plus body rows with fixed column widths.
func tableShape(id int, name string, x, y, cx int, rowHeight int, columns []tableColumn, rows [][]tableCell) string {
	var grid strings.Builder
	for _, column := range columns {
		fmt.Fprintf(&grid, `<a:gridCol w="%d"/>`, column.Width)
	}
	var body strings.Builder
	fmt.Fprintf(&body, `<a:tr h="%d">`, rowHeight)
	for _, column := range columns {
		body.WriteString(tableCellXML(tableCell{Text: column.Title, Bold: true, Color: "FFFFFF", Align: "ctr"}, "2563EB", 1100))
	}
	body.WriteString(`</a:tr>`)
	for index, row := range rows {
		fill := "FFFFFF"
		if index%2 == 1 {
			fill = "F8FAFC"
		}
		fmt.Fprintf(&body, `<a:tr h="%d">`, rowHeight)
		for _, cell := range row {
			body.WriteString(tableCellXML(cell, fill, 1000))
		}
		body.WriteString(`</a:tr>`)
	}
	height := rowHeight * (len(rows) + 1)
	return fmt.Sprintf(`<p:graphicFrame><p:nvGraphicFramePr><p:cNvPr id="%d" name="%s"/><p:cNvGraphicFramePr/><p:nvPr/></p:nvGraphicFramePr><p:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></p:xfrm><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/table"><a:tbl><a:tblPr firstRow="1"><a:noFill/><a:tableStyleId>{5C22544A-7EE6-4342-B048-85BDC9FD1C3A}</a:tableStyleId></a:tblPr><a:tblGrid>%s</a:tblGrid>%s</a:tbl></a:graphicData></a:graphic></p:graphicFrame>`,
		id, escapeXML(name), x, y, cx, height, grid.String(), body.String())
}

func tableCellXML(cell tableCell, fill string, size int) string {
	bold := "0"
	if cell.Bold {
		bold = "1"
	}
	color := cell.Color
	if color == "" {
		color = "1E293B"
	}
	align := cell.Align
	if align == "" {
		align = "l"
	}
	return `<a:tc><a:txBody><a:bodyPr/><a:lstStyle/><a:p><a:pPr algn="` + align + `"><a:buNone/></a:pPr>` +
		fmt.Sprintf(`<a:r><a:rPr lang="ko-KR" sz="%d" b="%s"><a:solidFill><a:srgbClr val="%s"/></a:solidFill><a:latin typeface="Aptos"/><a:ea typeface="Noto Sans KR"/></a:rPr><a:t>%s</a:t></a:r>`, size, bold, color, escapeXML(cell.Text)) +
		fmt.Sprintf(`<a:endParaRPr lang="ko-KR" sz="%d"/></a:p></a:txBody>`, size) +
		`<a:tcPr marT="45720" marB="45720" marL="82296" marR="82296" anchor="ctr"><a:lnL w="6350"><a:solidFill><a:srgbClr val="E2E8F0"/></a:solidFill></a:lnL><a:lnR w="6350"><a:solidFill><a:srgbClr val="E2E8F0"/></a:solidFill></a:lnR><a:lnT w="6350"><a:solidFill><a:srgbClr val="E2E8F0"/></a:solidFill></a:lnT><a:lnB w="6350"><a:solidFill><a:srgbClr val="E2E8F0"/></a:solidFill></a:lnB><a:solidFill><a:srgbClr val="` + fill + `"/></a:solidFill></a:tcPr></a:tc>`
}
