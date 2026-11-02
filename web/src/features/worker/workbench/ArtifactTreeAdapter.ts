import { FileTree, type GitStatusEntry } from "@pierre/trees";
import type { PatchReviewFile } from "./PatchReviewAdapter";

export interface ArtifactTreeInput {
  paths: string[];
  gitStatus: GitStatusEntry[];
}

export function buildArtifactTreeInput(files: PatchReviewFile[]): ArtifactTreeInput {
  return {
    paths: files.map((file) => file.path),
    gitStatus: files.map((file) => ({
      path: file.path,
      status: file.status === "added" ? "added" :
        file.status === "deleted" ? "deleted" :
          file.status === "renamed" ? "renamed" : "modified",
    })),
  };
}

export function createArtifactTree(
  files: PatchReviewFile[],
  selectedPath: string | undefined,
  onSelectionChange: (path: string) => void,
): FileTree {
  const input = buildArtifactTreeInput(files);
  return new FileTree({
    paths: input.paths,
    gitStatus: input.gitStatus,
    initialExpansion: "open",
    initialSelectedPaths: selectedPath ? [selectedPath] : [],
    onSelectionChange: (paths) => {
      const selected = [...paths].reverse().find((path) => input.paths.includes(path));
      if (selected) onSelectionChange(selected);
    },
    dragAndDrop: false,
    renaming: false,
    search: input.paths.length > 12,
    density: "compact",
  });
}
