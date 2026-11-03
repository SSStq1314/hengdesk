# 样本生成脚本（仅测试工具，不属于产品代码）。生成 Flat ODF，再用 LibreOffice 转 docx/pdf/xlsx/pptx。
import json, html
S = {
 "zh": {
  "title": "衡台知识库季度运营报告",
  "paras": [
   "本报告汇总了第三季度知识库的使用情况，包括文档入库数量、检索命中率以及用户满意度等核心指标。",
   "在权限控制方面，系统采用预过滤策略，确保用户只能检索到其有权访问的文档片段。",
   "下季度计划引入增量索引与自动过期机制，以降低重建索引带来的资源开销。",
  ],
  "cols": [
   "左栏：多栏排版是政府公文和期刊论文中常见的版式，解析器需要按阅读顺序输出文字，而不是按行横向拼接两栏内容。",
   "右栏：如果解析器把左右两栏的同一行拼在一起，切片后的语义会被破坏，从而直接影响向量检索的召回质量。",
  ],
  "table": [["部门","文档数","检索次数","命中率"],["财务部","1204","35210","92.4%"],["法务部","876","12045","88.1%"],["研发中心","5310","98022","95.7%"]],
 },
 "en": {
  "title": "HengDesk Knowledge Base Quarterly Report",
  "paras": [
   "This report summarizes knowledge base usage in the third quarter, including ingested documents, retrieval hit rate and user satisfaction.",
   "For access control the system applies a pre-filter so that users can only retrieve chunks from documents they are allowed to read.",
   "Next quarter we plan to add incremental indexing and automatic expiry to reduce the cost of rebuilding indexes.",
  ],
  "cols": [
   "Left column: multi-column layouts are common in journals and official documents, and a parser must emit text in reading order.",
   "Right column: if a parser merges the same line of both columns, chunk semantics break and vector retrieval recall suffers.",
  ],
  "table": [["Department","Docs","Queries","Hit rate"],["Finance","1204","35210","92.4%"],["Legal","876","12045","88.1%"],["R&D Center","5310","98022","95.7%"]],
 }}
e = html.escape
for lang, d in S.items():
    font = "Noto Serif CJK SC" if lang=="zh" else "Liberation Serif"
    rows = "".join("<table:table-row>"+"".join(f'<table:table-cell office:value-type="string"><text:p>{e(c)}</text:p></table:table-cell>' for c in r)+"</table:table-row>" for r in d["table"])
    body = f'<text:h text:outline-level="1">{e(d["title"])}</text:h>' + "".join(f"<text:p>{e(p)}</text:p>" for p in d["paras"])
    body += f'<table:table table:name="T1"><table:table-column table:number-columns-repeated="4"/>{rows}</table:table>'
    body += '<text:section text:style-name="Sect2" text:name="S2">' + f'<text:p>{e(d["cols"][0])}</text:p><text:p text:style-name="Brk">{e(d["cols"][1])}</text:p></text:section>'
    fodt = f'''<?xml version="1.0" encoding="UTF-8"?>
<office:document xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:style="urn:oasis:names:tc:opendocument:xmlns:style:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" xmlns:fo="urn:oasis:names:tc:opendocument:xmlns:xsl-fo-compatible:1.0" office:version="1.3" office:mimetype="application/vnd.oasis.opendocument.text">
<office:font-face-decls><style:font-face style:name="F" svg:font-family="'{font}'" xmlns:svg="urn:oasis:names:tc:opendocument:xmlns:svg-compatible:1.0"/></office:font-face-decls>
<office:styles><style:default-style style:family="paragraph"><style:text-properties style:font-name="F" style:font-name-asian="F" fo:font-size="11pt" style:font-size-asian="11pt"/></style:default-style></office:styles>
<office:automatic-styles>
<style:style style:name="Sect2" style:family="section"><style:section-properties><style:columns fo:column-count="2" fo:column-gap="1cm"/></style:section-properties></style:style>
<style:style style:name="Brk" style:family="paragraph"><style:paragraph-properties fo:break-before="column"/></style:style>
</office:automatic-styles>
<office:body><office:text>{body}</office:text></office:body></office:document>'''
    open(f"{lang}_report.fodt","w").write(fodt)
    # spreadsheet
    srows = "".join("<table:table-row>"+"".join((f'<table:table-cell office:value-type="float" office:value="{c}"><text:p>{c}</text:p></table:table-cell>' if c.isdigit() else f'<table:table-cell office:value-type="string"><text:p>{e(c)}</text:p></table:table-cell>') for c in r)+"</table:table-row>" for r in d["table"])
    open(f"{lang}_table.fods","w").write(f'''<?xml version="1.0" encoding="UTF-8"?>
<office:document xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" office:version="1.3" office:mimetype="application/vnd.oasis.opendocument.spreadsheet">
<office:body><office:spreadsheet><table:table table:name="{'统计' if lang=='zh' else 'Stats'}">{srows}</table:table></office:spreadsheet></office:body></office:document>''')
    # presentation: 2 slides
    def frame(txt,y): return f'<draw:frame svg:x="1cm" svg:y="{y}cm" svg:width="24cm" svg:height="3cm" presentation:class="outline"><draw:text-box><text:p>{e(txt)}</text:p></draw:text-box></draw:frame>'
    slides = f'<draw:page draw:name="p1">{frame(d["title"],1)}{frame(d["paras"][0],5)}</draw:page><draw:page draw:name="p2">{frame(d["paras"][1],1)}{frame(d["paras"][2],5)}</draw:page>'
    open(f"{lang}_slides.fodp","w").write(f'''<?xml version="1.0" encoding="UTF-8"?>
<office:document xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" xmlns:draw="urn:oasis:names:tc:opendocument:xmlns:drawing:1.0" xmlns:svg="urn:oasis:names:tc:opendocument:xmlns:svg-compatible:1.0" xmlns:presentation="urn:oasis:names:tc:opendocument:xmlns:presentation:1.0" office:version="1.3" office:mimetype="application/vnd.oasis.opendocument.presentation">
<office:body><office:presentation>{slides}</office:presentation></office:body></office:document>''')
    md = f"# {d['title']}\n\n" + "\n\n".join(d["paras"]) + "\n\n| " + " | ".join(d["table"][0]) + " |\n|" + "---|"*4 + "\n" + "".join("| "+" | ".join(r)+" |\n" for r in d["table"][1:]) + "\n" + "\n\n".join(d["cols"]) + "\n"
    open(f"{lang}_report.md","w").write(md)
    open(f"{lang}_report.txt","w").write(md.replace("# ","").replace("|"," "))
    json.dump(d, open(f"{lang}_truth.json","w"), ensure_ascii=False, indent=1)
