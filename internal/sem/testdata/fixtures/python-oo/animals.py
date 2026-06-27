from typing import overload


class Named:
    def name(self):
        return "thing"


class Loud:
    def shout(self):
        return "noise"


class Dog(Named, Loud):
    def shout(self):
        return "woof"


# @overload stubs are type-only and must NOT be emitted as symbols; only the
# implementation `describe` below should appear.
@overload
def describe(x: int) -> str: ...
@overload
def describe(x: str) -> str: ...
def describe(x):
    return str(x)
