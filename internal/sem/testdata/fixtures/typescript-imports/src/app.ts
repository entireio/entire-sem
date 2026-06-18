import { helper } from "./util"
import { readFileSync } from "fs"

export function run(): string {
  const raw = readFileSync("config.json", "utf8")
  return helper(raw)
}
