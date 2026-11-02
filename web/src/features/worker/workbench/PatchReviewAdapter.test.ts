import { describe, expect, it } from "vitest";
import { buildArtifactTreeInput, createArtifactTree } from "./ArtifactTreeAdapter";
import { parsePatchReview } from "./PatchReviewAdapter";

const multiFilePatch = `diff --git a/src/a.ts b/src/a.ts
index 1111111..2222222 100644
--- a/src/a.ts
+++ b/src/a.ts
@@ -1 +1,2 @@
-const a = 1;
+const a = 2;
+export { a };
diff --git a/docs/new.md b/docs/new.md
new file mode 100644
--- /dev/null
+++ b/docs/new.md
@@ -0,0 +1 @@
+# New
`;

describe("PatchReviewAdapter", () => {
  it("characterizes the pinned Pierre parser behind Switchback-owned file statistics", () => {
    const review = parsePatchReview(multiFilePatch);
    expect(review.files.map((file) => [file.path, file.status])).toEqual([
      ["src/a.ts", "modified"],
      ["docs/new.md", "added"],
    ]);
    expect(review).toMatchObject({ additions: 3, deletions: 1, hunkCount: 2 });
    expect(review.files[0].diff.hunks).toHaveLength(1);
  });

  it("characterizes the pinned tree input and selection model", () => {
    const files = parsePatchReview(multiFilePatch).files;
    expect(buildArtifactTreeInput(files)).toEqual({
      paths: ["src/a.ts", "docs/new.md"],
      gitStatus: [
        { path: "src/a.ts", status: "modified" },
        { path: "docs/new.md", status: "added" },
      ],
    });
    const selected: string[] = [];
    const tree = createArtifactTree(files, "src/a.ts", (path) => selected.push(path));
    expect(tree.getSelectedPaths()).toEqual(["src/a.ts"]);
    tree.getItem("docs/new.md")?.select();
    expect(selected.at(-1)).toBe("docs/new.md");
    tree.cleanUp();
  });

  it("keeps empty and malformed patches explicit", () => {
    expect(parsePatchReview("")).toEqual({ files: [], additions: 0, deletions: 0, hunkCount: 0 });
    expect(() => parsePatchReview("not a diff")).toThrow();
  });
});
