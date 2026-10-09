def valid_artifact:
  (.size | type == "number" and . > 0 and floor == .) and
  (.hash == "sha256" or .hash == "blake3") and
  (.digest | type == "string" and test("^[0-9a-f]{64}$")) and
  (.path | type == "string" and length > 0 and
    (test("(^/|\\\\|(^|/)\\.\\.?(/|$)|/$)") | not)) and
  (.providers | type == "array" and length > 0) and
  all(.providers[];
    .url | type == "string" and
    test("^https://github.com/" + $repo + "/releases/download/(v?[0-9]+\\.[0-9]+\\.[0-9]+|jq-[0-9]+\\.[0-9]+\\.[0-9]+|[0-9]{4}-[0-9]{2}-[0-9]{2})/[^/?#]+$")
  );

if .name == $name and
   (.platforms | type == "object" and
     has("linux-x86_64") and has("linux-aarch64") and
     has("macos-x86_64") and has("macos-aarch64")) and
   all(.platforms[]; valid_artifact)
then
  [.platforms[].providers[].url | capture("/releases/download/(?<version>[^/]+)/").version] | unique |
  if length == 1 then .[0] else error("Mixed release versions in launcher") end
else error("Invalid launcher structure, platform, digest, or release URL") end
