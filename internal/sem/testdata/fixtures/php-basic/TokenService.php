<?php

namespace App\Auth;

use App\Support\Str;

class TokenService
{
    public function validate(string $token): bool
    {
        return Str::length($token) > 0;
    }

    public function check(string $token): bool
    {
        return $this->validate($token);
    }
}
