export interface Shape {
  area(): number
}

export interface Drawable extends Shape {
  draw(): void
}

export abstract class Base {
  abstract describe(): string
}

export class Circle extends Base implements Drawable {
  area(): number {
    return 3.14
  }
  draw(): void {}
  describe(): string {
    return "circle"
  }
}
