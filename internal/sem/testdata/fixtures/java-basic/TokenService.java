package auth;

import java.util.Objects;

public class TokenService {
    public boolean validate(String token) {
        return Objects.nonNull(token) && !token.isEmpty();
    }

    public boolean check(String token) {
        return validate(token);
    }
}
