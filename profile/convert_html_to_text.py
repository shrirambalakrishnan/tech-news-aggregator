import sys, pathlib, html2text
h = html2text.HTML2Text()
h.ignore_links = True
h.ignore_images = True
for f in pathlib.Path(sys.argv[1]).glob("*.html"):
    md = h.handle(f.read_text())
    f.with_suffix(".md").write_text(md)
    print(f"{f.name} -> {f.stem}.md")