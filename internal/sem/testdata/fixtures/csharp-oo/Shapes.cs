using System;

namespace Shapes
{
    public interface IShape
    {
        double Area();
    }

    public abstract class Base
    {
        public abstract string Describe();
    }

    public class Circle : Base, IShape
    {
        public double Area()
        {
            return 3.14;
        }

        public override string Describe()
        {
            return "circle";
        }
    }
}
