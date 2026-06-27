export interface Shape {
  area(): number
}

export interface Drawable extends Shape {
  draw(): void
}

export abstract class Base {
  abstract describe(): string
  // base helper invoked by subclasses through `this` (inherited receiver call)
  label(): string {
    return "shape"
  }
}

export class Circle extends Base implements Drawable {
  area(): number {
    return 3.14
  }
  draw(): void {}
  describe(): string {
    // this.method() declared on the base class -> resolves up the chain
    return this.label()
  }
  // arrow-function class field: a callable member, not data
  scaled = (factor: number): number => {
    return this.area() * factor
  }
  // static factory, called below as Circle.create()
  static create = (): Circle => {
    return new Circle()
  }
}

export function make(): Circle {
  // ClassName.staticMethod(): class-qualified (static) call
  return Circle.create()
}
