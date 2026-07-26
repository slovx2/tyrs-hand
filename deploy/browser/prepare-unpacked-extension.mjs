import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdtemp, mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";

const [crxPath, outputPath, expectedId] = process.argv.slice(2);
if (!crxPath || !outputPath || !/^[a-p]{32}$/.test(expectedId || ""))
  throw new Error("usage: node prepare-unpacked-extension.mjs <extension.crx> <output-dir> <extension-id>");

const crx = await readFile(crxPath);
if (crx.length < 16 || crx.subarray(0, 4).toString("ascii") !== "Cr24")
  throw new Error("CRX magic is invalid");
if (crx.readUInt32LE(4) !== 3)
  throw new Error("only CRX3 is supported");

const headerLength = crx.readUInt32LE(8);
const zipOffset = 12 + headerLength;
if (zipOffset + 4 > crx.length || crx.readUInt32LE(zipOffset) !== 0x04034b50)
  throw new Error("CRX zip payload is invalid");

const header = crx.subarray(12, zipOffset);
const proof = readFields(header).find(field => field.number === 2 || field.number === 3);
if (!proof || proof.wireType !== 2)
  throw new Error("CRX signing proof is missing");
const publicKey = readFields(proof.value).find(field => field.number === 1 && field.wireType === 2)?.value;
if (!publicKey)
  throw new Error("CRX public key is missing");

const digest = createHash("sha256").update(publicKey).digest().subarray(0, 16);
const extensionId = [...digest].map(byte =>
  String.fromCharCode(97 + (byte >> 4), 97 + (byte & 0x0f))).join("");
if (extensionId !== expectedId)
  throw new Error(`CRX extension id mismatch: ${extensionId}`);

await mkdir(dirname(outputPath), { recursive: true });
const temporary = await mkdtemp(`${outputPath}.install-`);
try {
  const zipPath = join(temporary, "extension.zip");
  const unpackedPath = join(temporary, "unpacked");
  await writeFile(zipPath, crx.subarray(zipOffset), { mode: 0o600 });
  await mkdir(unpackedPath);
  execFileSync("/usr/bin/ditto", ["-x", "-k", zipPath, unpackedPath], { stdio: "inherit" });

  const manifestPath = join(unpackedPath, "manifest.json");
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  manifest.key = publicKey.toString("base64");
  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`, { mode: 0o644 });

  await rm(outputPath, { recursive: true, force: true });
  await rename(unpackedPath, outputPath);
} finally {
  await rm(temporary, { recursive: true, force: true });
}

console.log(outputPath);

function readFields(buffer) {
  const fields = [];
  let offset = 0;
  while (offset < buffer.length) {
    const tag = readVarint(buffer, offset);
    offset = tag.offset;
    const number = tag.value >>> 3;
    const wireType = tag.value & 7;
    if (!number)
      throw new Error("protobuf field number is invalid");
    if (wireType === 0) {
      const value = readVarint(buffer, offset);
      offset = value.offset;
      fields.push({ number, wireType, value: value.value });
      continue;
    }
    if (wireType === 2) {
      const length = readVarint(buffer, offset);
      offset = length.offset;
      const end = offset + length.value;
      if (end > buffer.length)
        throw new Error("protobuf field exceeds CRX header");
      fields.push({ number, wireType, value: buffer.subarray(offset, end) });
      offset = end;
      continue;
    }
    if (wireType === 1)
      offset += 8;
    else if (wireType === 5)
      offset += 4;
    else
      throw new Error(`unsupported protobuf wire type: ${wireType}`);
    if (offset > buffer.length)
      throw new Error("protobuf field exceeds CRX header");
  }
  return fields;
}

function readVarint(buffer, start) {
  let value = 0;
  let shift = 0;
  for (let offset = start; offset < buffer.length && shift <= 28; offset++) {
    const byte = buffer[offset];
    value |= (byte & 0x7f) << shift;
    if (!(byte & 0x80))
      return { value: value >>> 0, offset: offset + 1 };
    shift += 7;
  }
  throw new Error("protobuf varint is invalid");
}
