import { parsePatchFiles, type FileDiffMetadata } from "@pierre/diffs";

export type PatchFileStatus = "modified" | "added" | "deleted" | "renamed";

export interface PatchReviewFile {
  id: string;
  path: string;
  previousPath?: string;
  status: PatchFileStatus;
  additions: number;
  deletions: number;
  hunkCount: number;
  diff: FileDiffMetadata;
}

export interface PatchReviewModel {
  files: PatchReviewFile[];
  additions: number;
  deletions: number;
  hunkCount: number;
}

export function parsePatchReview(content: string): PatchReviewModel {
  if (content.length === 0) return { files: [], additions: 0, deletions: 0, hunkCount: 0 };
  const files = parsePatchFiles(content, "switchback-workbench", true)
    .flatMap((patch) => patch.files)
    .map(toPatchReviewFile);
  if (files.length === 0) throw new Error("Patch contains no reviewable files");
  return {
    files,
    additions: files.reduce((total, file) => total + file.additions, 0),
    deletions: files.reduce((total, file) => total + file.deletions, 0),
    hunkCount: files.reduce((total, file) => total + file.hunkCount, 0),
  };
}

function toPatchReviewFile(diff: FileDiffMetadata): PatchReviewFile {
  const path = canonicalPath(diff.name);
  return {
    id: `${statusFor(diff.type)}:${path}`,
    path,
    previousPath: diff.prevName ? canonicalPath(diff.prevName) : undefined,
    status: statusFor(diff.type),
    additions: diff.hunks.reduce((total, hunk) => total + hunk.additionLines, 0),
    deletions: diff.hunks.reduce((total, hunk) => total + hunk.deletionLines, 0),
    hunkCount: diff.hunks.length,
    diff,
  };
}

function canonicalPath(value: string): string {
  return value.replace(/\\/g, "/").replace(/^([ab])\//, "").replace(/^\/+/, "");
}

function statusFor(type: FileDiffMetadata["type"]): PatchFileStatus {
  if (type === "new") return "added";
  if (type === "deleted") return "deleted";
  if (type === "rename-pure" || type === "rename-changed") return "renamed";
  return "modified";
}
