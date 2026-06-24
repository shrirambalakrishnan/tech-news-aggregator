import sys, pathlib, fitz   # PyMuPDF imports as 'fitz'

folder = pathlib.Path(sys.argv[1])
for pdf in folder.glob("*.pdf"):
    doc = fitz.open(pdf)
    text = "\n".join(page.get_text() for page in doc)
    out = pdf.with_suffix(".txt")
    out.write_text(text)
    print(f"{pdf.name} -> {out.name} ({len(text)} chars)")