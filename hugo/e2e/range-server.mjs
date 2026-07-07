// Minimal static file server WITH Range support (python http.server lacks it;
// the RoaringRange reader requires it, as any real origin provides).
import { createServer } from "node:http";
import { stat, open } from "node:fs/promises";
import { join, normalize, extname } from "node:path";

const [root, port] = [process.argv[2], Number(process.argv[3])];
const types = {
  ".html": "text/html",
  ".js": "text/javascript",
  ".mjs": "text/javascript",
  ".css": "text/css",
  ".json": "application/json",
  ".wasm": "application/wasm",
};

createServer(async (req, res) => {
  try {
    let path = decodeURIComponent(new URL(req.url, "http://x").pathname);
    let file = normalize(join(root, path));
    if (!file.startsWith(normalize(root))) throw new Error("traversal");
    let st = await stat(file).catch(() => null);
    if (st && st.isDirectory()) {
      file = join(file, "index.html");
      st = await stat(file).catch(() => null);
    }
    if (!st) {
      res.writeHead(404).end("not found");
      return;
    }
    const type = types[extname(file)] || "application/octet-stream";
    const range = req.headers.range;
    const fh = await open(file);
    if (range) {
      const m = /bytes=(\d*)-(\d*)/.exec(range);
      let start = m[1] ? Number(m[1]) : 0;
      let end = m[2] ? Number(m[2]) : st.size - 1;
      if (end >= st.size) end = st.size - 1;
      res.writeHead(206, {
        "Content-Type": type,
        "Content-Range": `bytes ${start}-${end}/${st.size}`,
        "Content-Length": end - start + 1,
        "Accept-Ranges": "bytes",
      });
      fh.createReadStream({ start, end }).pipe(res);
    } else {
      res.writeHead(200, { "Content-Type": type, "Content-Length": st.size, "Accept-Ranges": "bytes" });
      fh.createReadStream().pipe(res);
    }
  } catch (e) {
    res.writeHead(500).end(String(e));
  }
}).listen(port, "127.0.0.1", () => console.log("range server on " + port));
