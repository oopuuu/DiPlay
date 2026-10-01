with open('common/src/main/java/com/shilapi/xcertplay/web/WebRemoteHtml.kt', 'r', encoding='utf-8') as f:
    content = f.read()

start_marker = 'fun getHtml(port: Int): String = """'
end_marker = '""".trimIndent()'
start = content.find(start_marker) + len(start_marker)
end = content.find(end_marker, start)
html = content[start:end].strip()

# Replace any Kotlin template expressions
html = html.replace('${port}', '8088')
html = html.replace('\\$', '$')

with open('pi3b+_linux/web/index.html', 'w', encoding='utf-8') as out:
    out.write(html)
print('Extracted index.html size:', len(html))
