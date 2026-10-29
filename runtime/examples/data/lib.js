// A CommonJS module loaded with require().
exports.greet = (name) => `hello, ${name} (from ${__filename.split("/").slice(-2).join("/")})`;
