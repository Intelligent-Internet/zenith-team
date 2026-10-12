"use strict";

const dns = require("node:dns");
const http = require("node:http");
const https = require("node:https");
const { BlockList, isIP } = require("node:net");
const { linkPreviewUrl } = require("../shared/link-preview.cjs");

const reservedAddresses = new BlockList();
for (const [address, prefix] of [
  ["0.0.0.0", 8],
  ["10.0.0.0", 8],
  ["100.64.0.0", 10],
  ["127.0.0.0", 8],
  ["169.254.0.0", 16],
  ["172.16.0.0", 12],
  ["192.0.0.0", 24],
  ["192.0.2.0", 24],
  ["192.88.99.0", 24],
  ["192.168.0.0", 16],
  ["198.18.0.0", 15],
  ["198.51.100.0", 24],
  ["203.0.113.0", 24],
  ["224.0.0.0", 3],
])
  reservedAddresses.addSubnet(address, prefix, "ipv4");
for (const [address, prefix] of [
  ["2001::", 23],
  ["2001:db8::", 32],
  ["2002::", 16],
  ["3fff::", 20],
])
  reservedAddresses.addSubnet(address, prefix, "ipv6");
const globalIPv6 = new BlockList();
globalIPv6.addSubnet("2000::", 3, "ipv6");

function isPublicAddress(address) {
  const family = isIP(address);
  if (family === 4) return !reservedAddresses.check(address, "ipv4");
  return (
    family === 6 &&
    globalIPv6.check(address, "ipv6") &&
    !reservedAddresses.check(address, "ipv6")
  );
}

// Return the checked addresses to the socket itself: a separate DNS check
// followed by an ordinary fetch would allow DNS rebinding into the local network.
function lookupPublicAddress(hostname, options, callback) {
  dns.lookup(hostname, { all: true }, (error, addresses) => {
    if (error) return callback(error);
    if (
      !addresses.length ||
      addresses.some(({ address }) => !isPublicAddress(address))
    )
      return callback(new Error("Link previews require a public address."));
    if (options.all) return callback(null, addresses);
    const address =
      addresses.find((entry) => entry.family === 4) ?? addresses[0];
    callback(null, address.address, address.family);
  });
}

function publicPreviewUrl(value) {
  const normalized = linkPreviewUrl(value);
  if (!normalized) throw new Error("Invalid link preview URL.");
  const url = new URL(normalized);
  const hostname = url.hostname.replace(/^\[|\]$/g, "").replace(/\.$/, "");
  if (isIP(hostname)) {
    if (!isPublicAddress(hostname))
      throw new Error("Link previews require a public address.");
  } else if (
    !hostname.includes(".") ||
    /\.(?:localhost|local|internal|lan|home|test|invalid|onion)$/.test(hostname)
  ) {
    throw new Error("Link previews require a public hostname.");
  }
  return url;
}

async function openPreviewResponse(value, signal) {
  let url = publicPreviewUrl(value);
  for (let redirects = 0; redirects <= 3; redirects++) {
    signal.throwIfAborted();
    const response = await new Promise((resolve, reject) => {
      const transport = url.protocol === "https:" ? https : http;
      // Node's direct transport has no browser cookies, auth session, proxy
      // credentials, or referrer. A fresh socket always uses the guarded lookup.
      const request = transport.get(url, {
        agent: false,
        lookup: lookupPublicAddress,
        signal,
        headers: {
          "User-Agent": "ZenithTeam-LinkPreview/1.0",
          "Accept-Encoding": "identity",
        },
      });
      request.once("response", resolve);
      request.once("error", reject);
    });
    if ([301, 302, 303, 307, 308].includes(response.statusCode)) {
      response.destroy();
      if (!response.headers.location) throw new Error("Missing redirect URL.");
      url = publicPreviewUrl(new URL(response.headers.location, url).href);
      continue;
    }
    if (
      response.statusCode < 200 ||
      response.statusCode >= 300 ||
      (response.headers["content-encoding"] &&
        response.headers["content-encoding"] !== "identity")
    ) {
      response.destroy();
      throw new Error("Link preview response is unavailable.");
    }
    return { response, url: url.href };
  }
  throw new Error("Too many link preview redirects.");
}

async function readPreviewHead(response) {
  const decoder = new TextDecoder();
  let html = "";
  let length = 0;
  try {
    for await (const chunk of response) {
      const remaining = 512_000 - length;
      html += decoder.decode(chunk.subarray(0, remaining), { stream: true });
      length += chunk.length;
      if (length >= 512_000 || /<\/head\s*>/i.test(html)) break;
    }
    return html + decoder.decode();
  } finally {
    response.destroy();
  }
}

async function readPreviewImage(response, limit) {
  const chunks = [];
  let length = 0;
  try {
    if (Number(response.headers["content-length"]) > limit)
      throw new Error("Link preview image is too large.");
    for await (const chunk of response) {
      length += chunk.length;
      if (length > limit) throw new Error("Link preview image is too large.");
      chunks.push(chunk);
    }
    return Buffer.concat(chunks, length);
  } finally {
    response.destroy();
  }
}

module.exports = { openPreviewResponse, readPreviewHead, readPreviewImage };
