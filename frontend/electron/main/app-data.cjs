const path = require("node:path");
const {
  getDesktopVariant,
} = require("../shared/desktop-variant.cjs");

function getUserDataPath(appDataPath, env = process.env) {
  return path.join(appDataPath, getUserDataDirectoryName(env));
}

function getUserDataDirectoryName(env = process.env) {
  return getDesktopVariant(env).userDataDirectoryName;
}

module.exports = {
  getUserDataDirectoryName,
  getUserDataPath,
};
