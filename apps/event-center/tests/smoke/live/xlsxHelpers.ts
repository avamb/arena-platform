/**
 * Reads one part of an .xlsx (a zip) without a zip library: the central
 * directory gives each entry's offset and size, the entry is raw deflate.
 * Enough for a test to look at xl/worksheets/sheet1.xml of a guest list.
 */
import { inflateRawSync } from "node:zlib";

export function xlsxPart(file: Buffer, name: string): string {
  // End of central directory: the last 0x06054b50 in the file.
  let eocd = -1;
  for (let i = file.length - 22; i >= 0; i--) {
    if (file.readUInt32LE(i) === 0x06054b50) {
      eocd = i;
      break;
    }
  }
  if (eocd < 0) throw new Error("not a zip file");
  const entries = file.readUInt16LE(eocd + 10);
  let p = file.readUInt32LE(eocd + 16);
  for (let n = 0; n < entries; n++) {
    if (file.readUInt32LE(p) !== 0x02014b50) throw new Error("bad central directory");
    const method = file.readUInt16LE(p + 10);
    const size = file.readUInt32LE(p + 20);
    const nameLen = file.readUInt16LE(p + 28);
    const extraLen = file.readUInt16LE(p + 30);
    const commentLen = file.readUInt16LE(p + 32);
    const local = file.readUInt32LE(p + 42);
    const entryName = file.subarray(p + 46, p + 46 + nameLen).toString("utf8");
    if (entryName === name) {
      const start = local + 30 + file.readUInt16LE(local + 26) + file.readUInt16LE(local + 28);
      const data = file.subarray(start, start + size);
      return (method === 8 ? inflateRawSync(data) : data).toString("utf8");
    }
    p += 46 + nameLen + extraLen + commentLen;
  }
  throw new Error(`no ${name} in the workbook`);
}
