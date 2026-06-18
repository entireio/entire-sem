pub trait Shape {
    fn area(&self) -> f64;
}

pub trait Drawable: Shape {
    fn draw(&self);
}

pub struct Circle {
    pub radius: f64,
}

impl Shape for Circle {
    fn area(&self) -> f64 {
        3.14 * self.radius * self.radius
    }
}

impl Drawable for Circle {
    fn draw(&self) {}
}
