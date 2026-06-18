<?php

namespace Shapes;

interface Shape
{
    public function area(): float;
}

abstract class Base
{
    abstract public function describe(): string;
}

class Circle extends Base implements Shape
{
    public function area(): float
    {
        return 3.14;
    }

    public function describe(): string
    {
        return "circle";
    }
}
