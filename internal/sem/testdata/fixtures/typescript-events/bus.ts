import { EventEmitter } from "events"

const bus = new EventEmitter()

// Publisher and subscriber reference the same channel:user.created node.
export function createUser(): void {
  bus.emit("user.created", { id: 1 })
}

export function onUserCreated(): void {
  bus.on("user.created", (user) => {
    notify(user)
  })
}

export function notify(user: unknown): void {
  void user
}
