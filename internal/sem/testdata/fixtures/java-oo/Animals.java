package zoo;

interface Named {
    String name();
}

interface Loud extends Named {
    String shout();
}

abstract class Animal implements Named {
    public String name() {
        return "animal";
    }
}

public class Dog extends Animal implements Loud {
    public String shout() {
        return "woof";
    }
}
