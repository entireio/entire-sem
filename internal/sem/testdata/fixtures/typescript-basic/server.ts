import { readFileSync } from "fs"

export function loadConfig(): string {
  return readFileSync("config.json", "utf8")
}

export function register(app: any): void {
  app.get("/users/:id", handleRoute)
}

export function handleRoute(): string {
  loadConfig()
  // Returning a path string is NOT a route registration.
  return "/users/{id}"
}
