class Named:
    def name(self):
        return "thing"


class Loud:
    def shout(self):
        return "noise"


class Dog(Named, Loud):
    def shout(self):
        return "woof"
